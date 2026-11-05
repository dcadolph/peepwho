// swift-tools-version: 6.0

import PackageDescription

let package = Package(
    name: "Peepwho",
    platforms: [.macOS(.v14)],
    targets: [
        .binaryTarget(name: "PeepwhoCore", path: "Frameworks/PeepwhoCore.xcframework"),
        .target(
            name: "PeepwhoKit",
            dependencies: ["PeepwhoCore"],
            linkerSettings: [.linkedLibrary("resolv"), .linkedFramework("Security")]
        ),
        .executableTarget(
            name: "Host",
            dependencies: ["PeepwhoKit"],
            exclude: ["Info.plist", "Host.entitlements"]
        ),
        .executableTarget(
            name: "Peepwho",
            dependencies: ["PeepwhoKit"],
            exclude: ["Info.plist", "Peepwho.entitlements"]
        ),
        .testTarget(
            name: "PeepwhoKitTests",
            dependencies: ["PeepwhoKit"]
        ),
    ],
    swiftLanguageModes: [.v5]
)
