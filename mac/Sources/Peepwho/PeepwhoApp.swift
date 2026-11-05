import PeepwhoKit
import SwiftUI

// PeepwhoApp is the menu bar app: a search field over the index and a settings window.
@main
struct PeepwhoApp: App {
    // body declares the menu bar item.
    var body: some Scene {
        MenuBarExtra("peepwho", systemImage: "person.2") {
            VStack(alignment: .leading, spacing: 8) {
                Text("peepwho").font(.headline)
                Button("Quit") { NSApp.terminate(nil) }
            }
            .padding(12)
            .frame(width: 320)
        }
        .menuBarExtraStyle(.window)
    }
}
