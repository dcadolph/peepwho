import AppKit
import WhoToAskKit
import SwiftUI

// PeopleView is the People sheet: everyone the index holds, sorted by name, with a filter and a
// button per link that opens a conversation in that person's app.
struct PeopleView: View {
    // model loads the people.
    @ObservedObject var model: SettingsModel
    // dismiss closes the sheet.
    @Environment(\.dismiss) private var dismiss
    // filter is the text typed in the filter field.
    @State private var filter = ""
    // failed says which link would not open, nil when none failed.
    @State private var failed: String?

    // body lays out the title, the filter, and one row per person.
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline) {
                Text("People in the index").font(.headline)
                Text(Count.words(model.people.count, "person", "people")).foregroundStyle(.secondary)
                Spacer()
                Button("Done") { dismiss() }.keyboardShortcut(.defaultAction)
            }
            TextField("Filter by name, title, or team", text: $filter)
                .textFieldStyle(.roundedBorder)
            if let error = model.peopleError {
                Text(error).foregroundStyle(.secondary)
            }
            List(model.people.filter { $0.matches(filter) }) { person in
                row(person)
            }
            if let failed {
                Text(failed).font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding()
        .frame(width: 520, height: 480)
        .task { await model.loadPeople() }
    }

    // row shows one person's name, role, and link buttons.
    private func row(_ person: ListedPerson) -> some View {
        HStack(alignment: .firstTextBaseline) {
            VStack(alignment: .leading, spacing: 2) {
                NameText(name: person.name, deactivated: person.isDeactivated)
                if !person.role.isEmpty {
                    RoleText(line: person.role, deactivated: person.isDeactivated).font(.caption)
                }
            }
            Spacer()
            ForEach(SearchModel.contactLinks(person.links, deactivated: person.isDeactivated), id: \.self) { link in
                Button(PersonView.title(link)) { open(link) }.controlSize(.small)
            }
        }
    }

    // open opens link in its own app through the same check the search uses, or says it could not.
    private func open(_ link: String) {
        guard let url = SearchModel.openable(link), NSWorkspace.shared.open(url) else {
            failed = "Could not open \(link)."
            return
        }
        failed = nil
    }
}
