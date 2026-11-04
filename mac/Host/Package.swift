// swift-tools-version: 6.0

import PackageDescription

let package = Package(
    name: "Host",
    platforms: [.macOS(.v14)],
    targets: [
        .binaryTarget(name: "PeepwhoCore", path: "Frameworks/PeepwhoCore.xcframework"),
        .executableTarget(
            name: "Host",
            dependencies: ["PeepwhoCore"],
            linkerSettings: [.linkedLibrary("resolv"), .linkedFramework("Security")]
        ),
    ],
    swiftLanguageModes: [.v5]
)
