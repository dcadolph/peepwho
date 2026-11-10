// swift-tools-version: 6.0

import PackageDescription

let package = Package(
    name: "WhoToAsk",
    platforms: [.macOS(.v14)],
    targets: [
        .binaryTarget(name: "WhoToAskCore", path: "Frameworks/WhoToAskCore.xcframework"),
        .target(
            name: "WhoToAskKit",
            dependencies: ["WhoToAskCore"],
            linkerSettings: [.linkedLibrary("resolv"), .linkedFramework("Security")]
        ),
        .executableTarget(
            name: "Host",
            dependencies: ["WhoToAskKit"],
            exclude: ["Info.plist", "Host.entitlements"]
        ),
        .executableTarget(
            name: "WhoToAsk",
            dependencies: ["WhoToAskKit"],
            exclude: ["Info.plist", "WhoToAsk.entitlements"]
        ),
        .testTarget(
            name: "WhoToAskKitTests",
            dependencies: ["WhoToAskKit"]
        ),
    ],
    swiftLanguageModes: [.v5]
)
