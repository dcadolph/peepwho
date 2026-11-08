import Foundation
import Testing

@testable import PeepwhoKit

// MockLoginItem answers like the system's login item, with a state and errors a test sets.
final class MockLoginItem: LoginItem {
    // state is what the system reports.
    var state: LoginState
    // afterEnable is the state a successful enable leaves.
    var afterEnable: LoginState = .on
    // failure is what enable and disable throw, nil when they work.
    var failure: Error?
    // enables and disables count the calls.
    var enables = 0
    var disables = 0

    // init starts in state.
    init(state: LoginState = .off) {
        self.state = state
    }

    // enable records the call, then fails or moves to afterEnable.
    func enable() throws {
        enables += 1
        if let failure { throw failure }
        state = afterEnable
    }

    // disable records the call, then fails or moves to off.
    func disable() throws {
        disables += 1
        if let failure { throw failure }
        state = .off
    }
}

// LoginItemTests check the Open at login switch in Settings against a system that can refuse.
@MainActor
struct LoginItemTests {
    // model builds settings over item with nothing open.
    func model(_ item: LoginItem?) -> SettingsModel {
        SettingsModel(dir: tempDir("peepwho-login"), open: { throw POSIXError(.ENOENT) }, secrets: MemorySecrets(),
                      places: MemoryPlaces(), loginItem: item)
    }

    // Test 0: The switch starts where the system is and never registers on its own.
    @Test func startsFromTheSystem() {
        let off = MockLoginItem()
        #expect(model(off).login == .off)
        #expect(off.enables == 0)
        #expect(model(MockLoginItem(state: .on)).login == .on)
        #expect(model(nil).login == nil)
    }

    // Test 1: Turning it on and off reaches the system and shows what the system says.
    @Test func turnsOnAndOff() {
        let item = MockLoginItem()
        let settings = model(item)
        settings.setOpensAtLogin(true)
        #expect(item.enables == 1)
        #expect(settings.login == .on)
        settings.setOpensAtLogin(false)
        #expect(item.disables == 1)
        #expect(settings.login == .off)
    }

    // Test 2: A refusal leaves the switch where the system left it and says why.
    @Test func refusalShows() {
        let item = MockLoginItem()
        item.failure = POSIXError(.EPERM)
        let settings = model(item)
        settings.setOpensAtLogin(true)
        #expect(settings.login == .off)
        #expect(settings.lines.first?.hasPrefix("Could not change opening at login:") == true)
    }

    // Test 3: A person who turned the app off in System Settings is told where to approve it, with
    // no error on top, and the note goes once it is approved.
    @Test func approvalExplains() async {
        let item = MockLoginItem(state: .needsApproval)
        item.failure = POSIXError(.EPERM)
        let settings = model(item)
        settings.setOpensAtLogin(true)
        #expect(settings.login == .needsApproval)
        #expect(!settings.lines.contains { $0.hasPrefix("Could not change") })
        #expect(settings.loginNote?.contains("Login Items") == true)
        item.state = .on
        await settings.loadStatus()
        #expect(settings.login == .on)
        #expect(settings.loginNote == nil)
    }

    // Test 4: A failed turn off shows even while approval is pending, since no note covers it.
    @Test func failedOffShows() {
        let item = MockLoginItem(state: .needsApproval)
        item.failure = POSIXError(.EPERM)
        let settings = model(item)
        settings.setOpensAtLogin(false)
        #expect(settings.lines.first?.hasPrefix("Could not change opening at login:") == true)
    }

    // Test 5: A change made in System Settings shows on the next status read.
    @Test func followsSystemSettings() async {
        let item = MockLoginItem(state: .on)
        let settings = model(item)
        item.state = .off
        await settings.loadStatus()
        #expect(settings.login == .off)
    }
}
