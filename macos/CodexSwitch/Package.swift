// swift-tools-version: 6.0
import PackageDescription

let package = Package(
  name: "CodexSwitch",
  platforms: [.macOS(.v13)],
  products: [.executable(name: "CodexSwitch", targets: ["CodexSwitchApp"])],
  targets: [
    .target(name: "CodexSwitchCore"),
    .executableTarget(name: "CodexSwitchApp", dependencies: ["CodexSwitchCore"]),
    .testTarget(name: "CodexSwitchCoreTests", dependencies: ["CodexSwitchCore", "CodexSwitchApp"]),
  ]
)
