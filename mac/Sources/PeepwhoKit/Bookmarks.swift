import Foundation

// BookmarkSlot names a place an app keeps a security-scoped bookmark for.
public enum BookmarkSlot: String, CaseIterable, Sendable {
    // git is the repository folder the git source reads.
    case git
    // csv is the org chart CSV the org-csv source reads.
    case csv
    // codeowners is the CODEOWNERS file the codeowners source reads.
    case codeowners

    // defaultsKey is the UserDefaults key the bookmark data lives under.
    public var defaultsKey: String { "bookmark.\(rawValue)" }
}

// Resolved is a bookmark turned back into a URL the sandbox lets the host read.
public struct Resolved {
    // url is the bookmarked file or folder, with access started.
    public let url: URL
    // stale reports that the bookmark had to be made again from the resolved URL.
    public let stale: Bool
}

// Bookmarks stores security-scoped bookmarks in UserDefaults so access to a folder or file the
// user picked survives a relaunch without another open panel.
public enum Bookmarks {
    // save makes an app-scoped bookmark for url and stores it in slot.
    public static func save(_ url: URL, in slot: BookmarkSlot) throws {
        let data = try url.bookmarkData(
            options: [.withSecurityScope, .securityScopeAllowOnlyReadAccess],
            includingResourceValuesForKeys: nil,
            relativeTo: nil
        )
        UserDefaults.standard.set(data, forKey: slot.defaultsKey)
    }

    // has reports whether slot holds a bookmark.
    public static func has(_ slot: BookmarkSlot) -> Bool {
        UserDefaults.standard.data(forKey: slot.defaultsKey) != nil
    }

    // resolve turns slot's bookmark back into a URL and starts access to it. A stale bookmark is
    // saved again from the resolved URL. It returns nil when slot holds no bookmark.
    public static func resolve(_ slot: BookmarkSlot) throws -> Resolved? {
        guard let data = UserDefaults.standard.data(forKey: slot.defaultsKey) else {
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
            try save(url, in: slot)
        }
        return Resolved(url: url, stale: stale)
    }

    // forget removes slot's bookmark.
    public static func forget(_ slot: BookmarkSlot) {
        UserDefaults.standard.removeObject(forKey: slot.defaultsKey)
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
