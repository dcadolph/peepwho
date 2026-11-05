import AppKit
import Foundation
import os
import PeepwhoCore
import PeepwhoKit
import Security

// HostModel runs the core calls the host proves and keeps the log the window shows. Core calls
// run on one serial queue, so the window never blocks and calls never overlap.
final class HostModel: ObservableObject {
    // lines is the log pane, oldest first.
    @Published var lines: [String] = []
    // queue runs every core call in order off the main thread.
    private let queue = DispatchQueue(label: "com.dcadolph.peepwho.host.core")
    // handle is the open data directory, nil until opened or after a wipe.
    private var handle: CoreHandle?
    // logger writes each line to the unified log, where the checks read it.
    private let logger = Logger(subsystem: "com.dcadolph.peepwho.host", category: "host")

    // dataDir is the data directory inside the app's sandbox container.
    let dataDir: URL = FileManager.default
        .urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        .appendingPathComponent("peepwho", isDirectory: true)

    // query is the fixed question asked after each launch index.
    static let query = "packfile"

    // say appends line to the pane and the unified log. Lines never hold a token or a key.
    func say(_ line: String) {
        logger.notice("\(line, privacy: .public)")
        DispatchQueue.main.async { self.lines.append(line) }
    }

    // run queues work on the core queue.
    func run(_ work: @escaping () -> Void) {
        queue.async(execute: work)
    }

    // now is the host clock in seconds since the Unix epoch, which the core takes as its clock.
    private var now: Int64 { Int64(Date().timeIntervalSince1970) }

    // launch opens the data directory, indexes both bookmarked places, asks the fixed question,
    // and refreshes, which is the sequence a relaunch must repeat without any panel.
    func launch() {
        run {
            self.say("launch: pid \(ProcessInfo.processInfo.processIdentifier), data dir \(self.dataDir.path)")
            guard self.openHandle() else { return }
            self.indexBookmark(.git)
            self.indexBookmark(.csv)
            self.ask()
            self.refreshNow()
        }
    }

    // openHandle reads or makes the index key and opens the data directory under it.
    private func openHandle() -> Bool {
        if handle != nil { return true }
        let hadKey = Keychain.get(SecretName.indexKey) != nil
        let key: String
        do {
            key = try IndexKey.load()
        } catch {
            say("index key: \(hadKey ? "read" : "save") failed: \(error)")
            return false
        }
        say(hadKey ? "index key: read from keychain" : "index key: generated and saved to keychain")
        var err: NSError?
        guard let h = CoreOpen(dataDir.path, key, &err) else {
            say("open failed: \(err?.localizedDescription ?? "no handle")")
            return false
        }
        h.setSecrets(KeychainSecrets())
        handle = h
        say("open: ok")
        return true
    }

    // indexBookmark resolves slot's bookmark and indexes it with the source that reads it.
    private func indexBookmark(_ slot: BookmarkSlot) {
        let resolved: Resolved?
        do {
            resolved = try Bookmarks.resolve(slot)
        } catch {
            say("bookmark \(slot.rawValue): resolve failed: \(error)")
            return
        }
        guard let resolved else {
            say("bookmark \(slot.rawValue): none saved, pick one")
            return
        }
        say("bookmark \(slot.rawValue): resolved\(resolved.stale ? " (stale, saved again)" : "")")
        switch slot {
        case .git:
            index("git", ["--repo-path", resolved.url.path, "--git-since-days", "10000"])
        case .csv:
            index("org-csv", ["--file", resolved.url.path])
        case .codeowners:
            index("codeowners", ["--file", resolved.url.path])
        }
    }

    // index runs one core index of source with flags, one argument per element.
    private func index(_ source: String, _ flags: [String]) {
        guard let handle else { return }
        do {
            let joined = flags.joined(separator: "\n")
            let raw = try call { handle.index(source, flags: joined, nowUnix: self.now, error: $0) }
            let view = decode(raw)
            say("index \(source): ok, scope \(view?["scope"] as? String ?? "?")")
        } catch {
            say("index \(source): failed: \(error.localizedDescription)")
        }
    }

    // ask asks the fixed question and shows the top three people. Names go to the pane only, so
    // the unified log carries the count and never a person.
    private func ask() {
        guard let handle else { return }
        do {
            let raw = try call { handle.ask(Self.query, limit: 3, error: $0) }
            let people = decode(raw)?["people"] as? [[String: Any]] ?? []
            let names = people.prefix(3).compactMap { $0["name"] as? String }
            logger.notice("ask \(Self.query, privacy: .public): \(names.count, privacy: .public) people")
            DispatchQueue.main.async {
                self.lines.append("ask \(Self.query): \(names.count) people: \(names.joined(separator: ", "))")
            }
        } catch {
            say("ask: failed: \(error.localizedDescription)")
        }
    }

    // refreshNow replays every recorded scope through the core and reports each one.
    private func refreshNow() {
        guard let handle else { return }
        do {
            let raw = try call { handle.refresh("", nowUnix: self.now, error: $0) }
            let view = decode(raw)
            let ok = view?["ok"] as? Bool ?? false
            say("refresh: ok \(ok)")
            for result in view?["results"] as? [[String: Any]] ?? [] {
                let source = result["source"] as? String ?? "?"
                let rok = result["ok"] as? Bool ?? false
                let detail = (result["error"] as? String).map { ": \($0)" } ?? ""
                say("refresh \(source): ok \(rok)\(detail)")
            }
        } catch {
            say("refresh: failed: \(error.localizedDescription)")
        }
    }

    // refresh queues a refresh.
    func refresh() {
        run {
            guard self.openHandle() else { return }
            self.refreshNow()
        }
    }

    // pick shows an open panel for slot, saves the bookmark, and indexes it.
    func pick(_ slot: BookmarkSlot) {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = slot == .git
        panel.canChooseFiles = slot == .csv
        panel.allowsMultipleSelection = false
        panel.message = slot == .git ? "Pick a git repository folder." : "Pick an org chart CSV."
        guard panel.runModal() == .OK, let url = panel.url else { return }
        adopt(url, as: slot)
    }

    // adopt saves a bookmark for a URL the user granted, by panel or by opening it with the app,
    // then indexes it.
    func adopt(_ url: URL, as slot: BookmarkSlot) {
        do {
            try Bookmarks.save(url, in: slot)
            say("bookmark \(slot.rawValue): saved")
        } catch {
            say("bookmark \(slot.rawValue): save failed: \(error.localizedDescription)")
            return
        }
        run {
            guard self.openHandle() else { return }
            self.indexBookmark(slot)
        }
    }

    // saveJira stores the Jira site, email, and token in the Keychain, then indexes the project
    // once so a later refresh has a recorded scope to replay.
    func saveJira(url: String, email: String, token: String, project: String, server: Bool) {
        run {
            do {
                try Keychain.set(SecretName.jiraURL, url)
                try Keychain.set(SecretName.jiraEmail, email)
                try Keychain.set(SecretName.jiraToken, token)
                self.say("jira: saved url, email, and token to keychain")
            } catch {
                self.say("jira: keychain save failed: \(error)")
                return
            }
            guard self.openHandle() else { return }
            var flags = ["--jira-project", project]
            if server { flags.insert("--jira-server", at: 0) }
            self.index("jira", flags)
        }
    }

    // forgetToken deletes the Jira token from the Keychain.
    func forgetToken() {
        run {
            Keychain.delete(SecretName.jiraToken)
            self.say("jira: token deleted from keychain")
        }
    }

    // wipe deletes the data directory through the core and reports whether it is gone.
    func wipe() {
        run {
            self.handle = nil
            var err: NSError?
            let raw = CoreWipe(self.dataDir.path, &err)
            if let err {
                self.say("wipe: failed: \(err.localizedDescription)")
                return
            }
            let removed = (self.decode(raw)?["removed"] as? [Any])?.count ?? 0
            let gone = !FileManager.default.fileExists(atPath: self.dataDir.path)
            self.say("wipe: removed \(removed) files, data dir \(gone ? "absent" : "still present")")
        }
    }

    // call runs a core method that reports failure through an error pointer and throws it.
    private func call(_ body: (NSErrorPointer) -> String) throws -> String {
        var err: NSError?
        let out = body(&err)
        if let err { throw err }
        return out
    }

    // decode parses a core JSON answer into a dictionary.
    private func decode(_ raw: String) -> [String: Any]? {
        guard let data = raw.data(using: .utf8) else { return nil }
        return (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
    }
}
