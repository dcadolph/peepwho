import Foundation

// DataDir locates the data directory an app keeps its index in.
public enum DataDir {
    // url is Application Support/peepwho, which a sandboxed app finds inside its own container.
    public static var url: URL {
        FileManager.default
            .urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("peepwho", isDirectory: true)
    }
}
