import AppKit
import WhoToAskKit
import SwiftUI

// WhoToAskApp is the menu bar app: a search field over the index and a settings window.
@main
struct WhoToAskApp: App {
    // app holds the search and the settings over one open index.
    @StateObject private var app = AppModel()

    // body declares the menu bar item and the settings window.
    var body: some Scene {
        MenuBarExtra {
            SearchView(model: app.search) {
                app.settings.reopenIfClosed()
                await app.settings.loadStatus()
                await app.settings.refreshIfStale()
            }
        } label: {
            Image(nsImage: MenuBarOwl.image)
        }
        .menuBarExtraStyle(.window)
        Settings {
            SettingsView(model: app.settings)
        }
    }
}

// AppModel opens the data directory in the app's container under the Keychain's index key and
// shares it between the search and the settings, which reopens it after a wipe.
@MainActor
final class AppModel: ObservableObject {
    // search drives the menu bar search.
    let search: SearchModel
    // settings drives the settings window and owns the open core.
    let settings: SettingsModel
    // timer checks for a stale index while the app runs.
    private var timer: Timer?
    // wake is the observer that checks for a stale index when the Mac wakes.
    private var wake: NSObjectProtocol?

    // init opens the index and wires the search to every core the settings open.
    init() {
        let dir = DataDir.url
        settings = SettingsModel(
            dir: dir,
            open: { try Core.open(dir: dir, key: try IndexKey.load(), secrets: KeychainSecrets()) },
            secrets: KeychainStore(),
            places: BookmarkStore(),
            sampleDir: Bundle.main.url(forResource: "SampleCompany", withExtension: nil),
            loginItem: SystemLoginItem()
        )
        search = SearchModel(core: settings.core, openError: settings.lines.first) { NSWorkspace.shared.open($0) }
        settings.onCore = { [weak search] core in search?.use(core) }
        settings.onSample = { [weak search] sample in search?.sample = sample }
        settings.onEmpty = { [weak search] empty in search?.empty = empty }
        settings.onRefreshFailure = { [weak search] failure in search?.refreshFailure = failure }
        let tick = max(15, min(settings.refreshInterval / 4, 300))
        timer = Timer.scheduledTimer(withTimeInterval: tick, repeats: true) { [weak self] _ in
            Task { @MainActor in await self?.settings.refreshIfStale() }
        }
        wake = NSWorkspace.shared.notificationCenter.addObserver(
            forName: NSWorkspace.didWakeNotification, object: nil, queue: .main
        ) { [weak self] _ in
            Task { @MainActor in
                self?.settings.reopenIfClosed()
                await self?.settings.refreshIfStale()
            }
        }
    }
}
