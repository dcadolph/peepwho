import Foundation

// SecretStore keeps source tokens and site addresses by the names the core reads them under.
public protocol SecretStore {
    // get returns the value stored under name, or nil when there is none.
    func get(_ name: String) -> String?
    // set stores value under name, replacing any value already there.
    func set(_ name: String, _ value: String) throws
    // delete removes the value stored under name.
    func delete(_ name: String)
}

// KeychainStore keeps secrets in the app's Keychain items.
public struct KeychainStore: SecretStore {
    // init makes a store over the running app's Keychain service.
    public init() {}

    // get returns the Keychain item stored under name.
    public func get(_ name: String) -> String? { Keychain.get(name) }

    // set stores value as the Keychain item name.
    public func set(_ name: String, _ value: String) throws { try Keychain.set(name, value) }

    // delete removes the Keychain item name.
    public func delete(_ name: String) { Keychain.delete(name) }
}

// PlaceStore keeps the folders and files the user picked, with access that survives a relaunch.
public protocol PlaceStore {
    // save remembers url for slot.
    func save(_ url: URL, in slot: BookmarkSlot) throws
    // resolve returns slot's place with access started, or nil when nothing is saved.
    func resolve(_ slot: BookmarkSlot) throws -> URL?
    // release ends the access resolve started.
    func release(_ url: URL)
    // forget drops slot's place.
    func forget(_ slot: BookmarkSlot)
    // has reports whether slot holds a place.
    func has(_ slot: BookmarkSlot) -> Bool
}

// BookmarkStore keeps places as security-scoped bookmarks in UserDefaults.
public struct BookmarkStore: PlaceStore {
    // init makes a store over the app's bookmarks.
    public init() {}

    // save stores a bookmark for url in slot.
    public func save(_ url: URL, in slot: BookmarkSlot) throws { try Bookmarks.save(url, in: slot) }

    // resolve turns slot's bookmark back into a URL with access started.
    public func resolve(_ slot: BookmarkSlot) throws -> URL? { try Bookmarks.resolve(slot)?.url }

    // release stops the security-scoped access to url.
    public func release(_ url: URL) { url.stopAccessingSecurityScopedResource() }

    // forget removes slot's bookmark.
    public func forget(_ slot: BookmarkSlot) { Bookmarks.forget(slot) }

    // has reports whether slot holds a bookmark.
    public func has(_ slot: BookmarkSlot) -> Bool { Bookmarks.has(slot) }
}
