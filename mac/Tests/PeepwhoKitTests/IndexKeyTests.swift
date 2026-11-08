import Foundation
import Security
import Testing

@testable import PeepwhoKit

// IndexKeyTests pin when the index key may be created, since replacing it locks the index.
struct IndexKeyTests {
    // aFailedReadWritesNothing checks a locked Keychain throws and never replaces the key.
    @Test func aFailedReadWritesNothing() {
        var writes = 0
        #expect(throws: KeychainError.self) {
            try IndexKey.load(
                read: { _ in throw KeychainError(status: errSecInteractionNotAllowed) },
                write: { _, _ in writes += 1 }
            )
        }
        #expect(writes == 0)
    }

    // aStoredKeyComesBackUnchanged checks an existing key is returned and not written again.
    @Test func aStoredKeyComesBackUnchanged() throws {
        var writes = 0
        let key = try IndexKey.load(read: { _ in "stored" }, write: { _, _ in writes += 1 })
        #expect(key == "stored")
        #expect(writes == 0)
    }

    // aMissingKeyIsMadeAndSaved checks a store with no key gets a new 32-byte key, saved once.
    @Test func aMissingKeyIsMadeAndSaved() throws {
        var saved: [String: String] = [:]
        let key = try IndexKey.load(read: { _ in nil }, write: { saved[$0] = $1 })
        #expect(saved[SecretName.indexKey] == key)
        #expect(Data(base64Encoded: key)?.count == 32)
    }

    // aFailedWriteThrows checks a key that could not be saved is never used.
    @Test func aFailedWriteThrows() {
        #expect(throws: KeychainError.self) {
            try IndexKey.load(read: { _ in nil }, write: { _, _ in throw KeychainError(status: errSecAuthFailed) })
        }
    }
}
