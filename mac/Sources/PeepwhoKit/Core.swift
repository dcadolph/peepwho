import Foundation
import PeepwhoCore

// Core is an opened data directory with typed, async calls off the main thread. Questions run on
// a concurrent queue and Index and Refresh on a serial one, so a refresh never overlaps an index
// and a question never waits behind either: the handle answers from the old index while a
// refresh builds the new one.
public final class Core: @unchecked Sendable {
    // handle is the bound Go handle every call goes through.
    private let handle: CoreHandle
    // reads runs questions, several at once.
    private let reads = DispatchQueue(label: "com.dcadolph.peepwho.core.reads", attributes: .concurrent)
    // writes runs Index and Refresh one at a time.
    private let writes = DispatchQueue(label: "com.dcadolph.peepwho.core.writes")

    // init wraps a handle already opened.
    private init(handle: CoreHandle) {
        self.handle = handle
    }

    // open opens the data directory at dir sealed with key, and reads source tokens from secrets.
    public static func open(dir: URL, key: String, secrets: CoreSecretsProtocol) throws -> Core {
        var err: NSError?
        guard let h = CoreOpen(dir.path, key, &err) else {
            throw err ?? CoreError.noHandle
        }
        h.setSecrets(secrets)
        return Core(handle: h)
    }

    // wipe deletes every file the data directory at dir holds.
    public static func wipe(dir: URL) throws -> WipeResult {
        var err: NSError?
        let raw = CoreWipe(dir.path, &err)
        if let err { throw err }
        return try decode(raw)
    }

    // ask answers who to talk to and where to ask about query.
    public func ask(_ query: String, limit: Int = 5) async throws -> AskAnswer {
        try await run { self.handle.ask(query, limit: limit, error: $0) }
    }

    // search finds people by name or keyword.
    public func search(_ query: String, limit: Int = 5) async throws -> SearchAnswer {
        try await run { self.handle.search(query, limit: limit, error: $0) }
    }

    // person returns everything the index knows about the person with id.
    public func person(_ id: String) async throws -> PersonDetail {
        try await run { self.handle.person(id, error: $0) }
    }

    // recall returns the conversations person took part in about query.
    public func recall(_ person: String, query: String, limit: Int = 5) async throws -> RecallAnswer {
        try await run { self.handle.recall(person, query: query, limit: limit, error: $0) }
    }

    // status reports what the index holds and how old it is at now.
    public func status(now: Date = Date()) async throws -> IndexStatus {
        try await run { self.handle.status(Self.unix(now), error: $0) }
    }

    // changes reports what changed in the index after since.
    public func changes(since: Date) async throws -> ChangesAnswer {
        try await run { self.handle.changes(since: Self.unix(since), error: $0) }
    }

    // index reads one source with the command line's index flags and merges it into the index.
    public func index(_ source: String, flags: [String], now: Date = Date()) async throws -> IndexResult {
        let joined = flags.joined(separator: "\n")
        return try await run(on: writes) { self.handle.index(source, flags: joined, nowUnix: Self.unix(now), error: $0) }
    }

    // refresh replays every recorded run of sources, or of every source when sources is empty.
    public func refresh(_ sources: [String] = [], now: Date = Date()) async throws -> RefreshResult {
        let joined = sources.joined(separator: ",")
        return try await run(on: writes) { self.handle.refresh(joined, nowUnix: Self.unix(now), error: $0) }
    }

    // run calls body on queue, the read queue unless given, and decodes its JSON answer.
    private func run<T: Decodable>(on queue: DispatchQueue? = nil,
                                   _ body: @escaping (NSErrorPointer) -> String) async throws -> T {
        try await withCheckedThrowingContinuation { cont in
            (queue ?? reads).async {
                var err: NSError?
                let raw = body(&err)
                if let err {
                    cont.resume(throwing: err)
                    return
                }
                cont.resume(with: Result { try Self.decode(raw) })
            }
        }
    }

    // unix is t in whole seconds since the Unix epoch, the clock the core takes.
    static func unix(_ t: Date) -> Int64 {
        Int64(t.timeIntervalSince1970)
    }

    // decode parses a core JSON answer into T.
    public static func decode<T: Decodable>(_ raw: String) throws -> T {
        try JSONDecoder().decode(T.self, from: Data(raw.utf8))
    }
}

// CoreError reports a failure the core gave no error for.
public enum CoreError: Error, CustomStringConvertible {
    // noHandle means Open returned neither a handle nor an error.
    case noHandle

    // description says what went wrong.
    public var description: String {
        switch self {
        case .noHandle:
            return "core: open returned no handle"
        }
    }
}
