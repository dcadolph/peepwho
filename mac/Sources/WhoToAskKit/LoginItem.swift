import ServiceManagement

// LoginState says whether the app opens at login.
public enum LoginState: Equatable, Sendable {
    case off
    case on
    case needsApproval
}

// LoginItem turns opening at login on and off.
public protocol LoginItem {
    // state reads the current setting, which a person can also change in System Settings.
    var state: LoginState { get }
    // enable asks the system to open the app at login.
    func enable() throws
    // disable asks the system to stop opening the app at login.
    func disable() throws
}

// SystemLoginItem is the app's own entry in the system's login items.
public struct SystemLoginItem: LoginItem {
    // init makes the entry for the running app.
    public init() {}

    // state maps the system's status. An app the system cannot find reads as off.
    public var state: LoginState {
        switch SMAppService.mainApp.status {
        case .enabled:
            return .on
        case .requiresApproval:
            return .needsApproval
        default:
            return .off
        }
    }

    // enable registers the app.
    public func enable() throws { try SMAppService.mainApp.register() }

    // disable unregisters the app.
    public func disable() throws { try SMAppService.mainApp.unregister() }

    // openSettings shows the Login Items page, where a person approves the app.
    public static func openSettings() { SMAppService.openSystemSettingsLoginItems() }
}
