import Foundation

// BookmarkSlot names a place an app keeps a security-scoped bookmark for.
public enum BookmarkSlot: String, CaseIterable, Sendable {
    // git is the repository folder the git source reads.
    case git
    // csv is the org chart CSV the org-csv source reads.
    case csv
    // codeowners is the CODEOWNERS file the codeowners source reads.
    case codeowners

    // source is the source that reads this place.
    public var source: String {
        switch self {
        case .git:
            return "git"
        case .csv:
            return "org-csv"
        case .codeowners:
            return "codeowners"
        }
    }

    // storeKey is the name the bookmark lives under in the secret store.
    public var storeKey: String { "bookmark.\(rawValue)" }
}

// Resolved is a bookmark turned back into a URL the sandbox lets the host read.
public struct Resolved {
    // url is the bookmarked file or folder, with access started.
    public let url: URL
    // stale reports that the bookmark had to be made again from the resolved URL.
    public let stale: Bool
}

// Bookmarks stores security-scoped bookmarks in the secret store so access to a folder or file the
// user picked survives a relaunch without another open panel. A bookmark carries the path it
// points at, so it stays out of the plain preferences file beside the sealed index.
public enum Bookmarks {
    // save makes an app-scoped bookmark for url and stores it in slot.
    public static func save(_ url: URL, in slot: BookmarkSlot, store: SecretStore = KeychainStore()) throws {
        let data = try url.bookmarkData(
            options: [.withSecurityScope, .securityScopeAllowOnlyReadAccess],
            includingResourceValuesForKeys: nil,
            relativeTo: nil
        )
        try store.set(slot.storeKey, data.base64EncodedString())
    }

    // has reports whether slot holds a bookmark.
    public static func has(_ slot: BookmarkSlot, store: SecretStore = KeychainStore()) -> Bool {
        store.get(slot.storeKey) != nil
    }

    // resolve turns slot's bookmark back into a URL and starts access to it. A stale bookmark is
    // saved again from the resolved URL. It returns nil when slot holds no bookmark.
    public static func resolve(_ slot: BookmarkSlot, store: SecretStore = KeychainStore()) throws -> Resolved? {
        guard let text = store.get(slot.storeKey), let data = Data(base64Encoded: text) else {
            return nil
        }
        var stale = false
        let url = try URL(
            resolvingBookmarkData: data,
            options: [.withSecurityScope],
            relativeTo: nil,
            bookmarkDataIsStale: &stale
        )
        guard url.startAccessingSecurityScopedResource() else {
            throw BookmarkError.noAccess(slot)
        }
        if stale {
            try save(url, in: slot, store: store)
        }
        return Resolved(url: url, stale: stale)
    }

    // forget removes slot's bookmark.
    public static func forget(_ slot: BookmarkSlot, store: SecretStore = KeychainStore()) {
        store.delete(slot.storeKey)
    }
}

// BookmarkError reports a bookmark that resolved but gave no access.
public enum BookmarkError: Error, CustomStringConvertible {
    // noAccess means startAccessingSecurityScopedResource refused the slot's URL.
    case noAccess(BookmarkSlot)

    // description names the slot that gave no access.
    public var description: String {
        switch self {
        case let .noAccess(slot):
            return "bookmark \(slot.rawValue) resolved but the sandbox gave no access"
        }
    }
}
