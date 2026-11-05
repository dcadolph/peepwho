import AppKit
import PeepwhoKit
import SwiftUI

// PeepwhoApp is the menu bar app: a search field over the index and a settings window.
@main
struct PeepwhoApp: App {
    // app holds the search and the settings over one open index.
    @StateObject private var app = AppModel()

    // body declares the menu bar item and the settings window.
    var body: some Scene {
        MenuBarExtra("peepwho", systemImage: "person.2") {
            SearchView(model: app.search)
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

    // init opens the index and wires the search to every core the settings open.
    init() {
        let dir = DataDir.url
        settings = SettingsModel(
            dir: dir,
            open: { try Core.open(dir: dir, key: try IndexKey.load(), secrets: KeychainSecrets()) },
            secrets: KeychainStore(),
            places: BookmarkStore()
        )
        search = SearchModel(core: settings.core, openError: settings.lines.first) { NSWorkspace.shared.open($0) }
        settings.onCore = { [weak search] core in search?.use(core) }
    }
}
