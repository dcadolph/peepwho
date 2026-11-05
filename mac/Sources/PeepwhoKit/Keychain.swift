import Foundation
import Security

// Keychain keeps an app's secrets as generic passwords in the login keychain, one item per
// name. The data protection keychain needs a provisioning profile, which a Developer ID build
// without one does not have, and the login keychain ties each item to the app's signature.
public enum Keychain {
    // service is the Keychain service every item is stored under: the running app's bundle id, so
    // two apps built from this package never read each other's items.
    static let service = Bundle.main.bundleIdentifier ?? "com.dcadolph.peepwho"

    // get returns the secret stored under name, or nil when there is none.
    public static func get(_ name: String) -> String? {
        var query = baseQuery(name)
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &result) == errSecSuccess,
              let data = result as? Data
        else {
            return nil
        }
        return String(data: data, encoding: .utf8)
    }

    // set replaces the secret stored under name.
    public static func set(_ name: String, _ value: String) throws {
        SecItemDelete(baseQuery(name) as CFDictionary)
        var item = baseQuery(name)
        item[kSecValueData as String] = Data(value.utf8)
        item[kSecAttrLabel as String] = "\(service) \(name)"
        let status = SecItemAdd(item as CFDictionary, nil)
        guard status == errSecSuccess else {
            throw KeychainError(status: status)
        }
    }

    // delete removes the secret stored under name, if any.
    public static func delete(_ name: String) {
        SecItemDelete(baseQuery(name) as CFDictionary)
    }

    // baseQuery identifies the item for name.
    private static func baseQuery(_ name: String) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: name,
        ]
    }
}

// KeychainError reports a failed Keychain write by its OSStatus.
public struct KeychainError: Error, CustomStringConvertible {
    // status is the OSStatus SecItemAdd returned.
    public let status: OSStatus

    // description names the status in words when Security has a message for it.
    public var description: String {
        let text = SecCopyErrorMessageString(status, nil) as String? ?? "unknown"
        return "keychain status \(status): \(text)"
    }
}
