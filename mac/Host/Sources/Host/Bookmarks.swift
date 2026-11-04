import Foundation

// BookmarkSlot names a place the host keeps a security-scoped bookmark for.
enum BookmarkSlot: String, CaseIterable {
    // git is the repository folder the git source reads.
    case git
    // csv is the org chart CSV the org-csv source reads.
    case csv

    // defaultsKey is the UserDefaults key the bookmark data lives under.
    var defaultsKey: String { "bookmark.\(rawValue)" }
}

// Resolved is a bookmark turned back into a URL the sandbox lets the host read.
struct Resolved {
    // url is the bookmarked file or folder, with access started.
    let url: URL
    // stale reports that the bookmark had to be made again from the resolved URL.
    let stale: Bool
}

// Bookmarks stores security-scoped bookmarks in UserDefaults so access to a folder or file the
// user picked survives a relaunch without another open panel.
enum Bookmarks {
    // save makes an app-scoped bookmark for url and stores it in slot.
    static func save(_ url: URL, in slot: BookmarkSlot) throws {
        let data = try url.bookmarkData(
            options: [.withSecurityScope, .securityScopeAllowOnlyReadAccess],
            includingResourceValuesForKeys: nil,
            relativeTo: nil
        )
        UserDefaults.standard.set(data, forKey: slot.defaultsKey)
    }

    // has reports whether slot holds a bookmark.
    static func has(_ slot: BookmarkSlot) -> Bool {
        UserDefaults.standard.data(forKey: slot.defaultsKey) != nil
    }

    // resolve turns slot's bookmark back into a URL and starts access to it. A stale bookmark is
    // saved again from the resolved URL. It returns nil when slot holds no bookmark.
    static func resolve(_ slot: BookmarkSlot) throws -> Resolved? {
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
    static func forget(_ slot: BookmarkSlot) {
        UserDefaults.standard.removeObject(forKey: slot.defaultsKey)
    }
}

// BookmarkError reports a bookmark that resolved but gave no access.
enum BookmarkError: Error, CustomStringConvertible {
    // noAccess means startAccessingSecurityScopedResource refused the slot's URL.
    case noAccess(BookmarkSlot)

    // description names the slot that gave no access.
    var description: String {
        switch self {
        case let .noAccess(slot):
            return "bookmark \(slot.rawValue) resolved but the sandbox gave no access"
        }
    }
}
