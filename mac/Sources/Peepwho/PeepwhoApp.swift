import AppKit
import PeepwhoKit
import SwiftUI

// PeepwhoApp is the menu bar app: a search field over the index and a settings window.
@main
struct PeepwhoApp: App {
    // search drives the menu bar search over the app's own index.
    @StateObject private var search = PeepwhoApp.openSearch()

    // body declares the menu bar item.
    var body: some Scene {
        MenuBarExtra("peepwho", systemImage: "person.2") {
            SearchView(model: search)
        }
        .menuBarExtraStyle(.window)
    }

    // openSearch opens the data directory in the app's container under the Keychain's index key
    // and builds the search over it. A failure to open shows in the menu instead of the results.
    @MainActor
    static func openSearch() -> SearchModel {
        let opener: (URL) -> Bool = { NSWorkspace.shared.open($0) }
        do {
            let key = try IndexKey.load()
            let core = try Core.open(dir: DataDir.url, key: key, secrets: KeychainSecrets())
            return SearchModel(core: core, opener: opener)
        } catch {
            return SearchModel(core: nil, openError: "The index did not open: \(error.localizedDescription)",
                               opener: opener)
        }
    }
}
