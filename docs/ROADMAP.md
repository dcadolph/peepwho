# Roadmap

peepwho answers "who do I talk to about X" from the data it has indexed. The next
versions widen what it can see. Because every source is one connector against a
single interface, each item below is additive and changes nothing downstream.

## More sources

GitHub, Jira, Confluence, PagerDuty, and git history are shipped. Still to
come, roughly in order of how many orgs they unlock:

- Microsoft 365: Teams messages and Outlook mail through one Graph API
  connector, so Microsoft-centric orgs get first-class coverage.
- Google Workspace: Docs, Drive, and Groups activity.
- Opsgenie: on-call schedules and service owners.
- Notion, Linear, and GitLab as the connector surface grows.

Each maps its data to people, teams, topics, and channels, and joins other
sources by email or an alias file, so one person stays one entry across them.

## Engine

- Memory-mapped vector store: embeddings are int8-quantized in the index today,
  a quarter of their float32 size; a separate memory-mapped store would let a
  very large graph search without loading every vector into memory.

## Experience

- Result deep links: open a channel or a profile directly from an answer.

## Personal digest

A second way to ask: "what did I miss that matters to me." Instead of a topic
query returning people, the digest scans the channels the running user can see,
scores each message for personal relevance, and returns a ranked, actionable
roll-up on demand, on a schedule, or live. It reuses the Slack client, the
messaging client, the LLM layer, and the policy egress rules, and adds only a
per-message relevance path beside the existing pipeline. See DIGEST.md for the
full plan.
