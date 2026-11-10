import Foundation
import WhoToAskCore

// SecretName holds the names secrets are stored under: the environment variable names the command
// line reads, which the core asks the app's Secrets for. They match the env names in the Go tree.
public enum SecretName {
    // indexKey holds the base64 key the data directory is sealed with.
    public static let indexKey = "WHOTOASK_INDEX_KEY"
    // slackToken holds the Slack user or bot token.
    public static let slackToken = "WHOTOASK_SLACK_TOKEN"
    // githubToken holds the GitHub token.
    public static let githubToken = "WHOTOASK_GITHUB_TOKEN"
    // jiraURL holds the Jira site URL.
    public static let jiraURL = "WHOTOASK_JIRA_URL"
    // jiraEmail holds the Jira account email.
    public static let jiraEmail = "WHOTOASK_JIRA_EMAIL"
    // jiraToken holds the Jira API token.
    public static let jiraToken = "WHOTOASK_JIRA_TOKEN"
    // confluenceURL holds the Confluence site URL.
    public static let confluenceURL = "WHOTOASK_CONFLUENCE_URL"
    // confluenceEmail holds the Confluence account email.
    public static let confluenceEmail = "WHOTOASK_CONFLUENCE_EMAIL"
    // confluenceToken holds the Confluence API token.
    public static let confluenceToken = "WHOTOASK_CONFLUENCE_TOKEN"
    // pagerdutyToken holds the PagerDuty API token.
    public static let pagerdutyToken = "WHOTOASK_PAGERDUTY_TOKEN"
    // scopes holds what each token source reads, as JSON. The core never asks for it.
    public static let scopes = "WHOTOASK_APP_SCOPES"

    // required names the secrets source cannot run without, none for a source reading a place.
    public static func required(by source: String) -> [String] {
        switch source {
        case "slack":
            return [slackToken]
        case "github":
            return [githubToken]
        case "jira":
            return [jiraURL, jiraToken]
        case "confluence":
            return [confluenceURL, confluenceToken]
        case "pagerduty":
            return [pagerdutyToken]
        default:
            return []
        }
    }
}

// KeychainSecrets hands the core the app's Keychain items by name, so the core reads tokens
// without learning where they live.
public final class KeychainSecrets: NSObject, CoreSecretsProtocol {
    // get returns the Keychain item stored under name, or empty when there is none.
    public func get(_ name: String?) -> String {
        guard let name else { return "" }
        return Keychain.get(name) ?? ""
    }
}

// IndexKey reads the data directory's key from the Keychain, making and saving one the first time.
public enum IndexKey {
    // load returns the stored key, or a new random 32-byte key saved first when the store says it
    // has none. A failed read throws and writes nothing, since replacing a key the Keychain only
    // refused to hand over would lock the index forever.
    public static func load(read: (String) throws -> String? = Keychain.read,
                            write: (String, String) throws -> Void = Keychain.set) throws -> String {
        if let stored = try read(SecretName.indexKey) {
            return stored
        }
        var raw = Data(count: 32)
        let status = raw.withUnsafeMutableBytes {
            SecRandomCopyBytes(kSecRandomDefault, 32, $0.baseAddress!)
        }
        guard status == errSecSuccess else {
            throw KeychainError(status: status)
        }
        let key = raw.base64EncodedString()
        try write(SecretName.indexKey, key)
        return key
    }
}
