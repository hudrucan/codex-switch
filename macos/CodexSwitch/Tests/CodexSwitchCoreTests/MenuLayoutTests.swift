import AppKit
import Foundation
import SwiftUI
import Testing

@testable import CodexSwitchApp

@Suite @MainActor
struct MenuLayoutTests {
  @Test(arguments: [2, 8])
  func accountListHasRoomForVisibleQuotaRows(accountCount: Int) async throws {
    var root = try #require(JSONSerialization.jsonObject(with: statusData()) as? [String: Any])
    var accounts = try #require(root["accounts"] as? [[String: Any]])
    for index in 2..<accountCount {
      var account = accounts[1]
      account["id"] = "synthetic-\(index)"
      account["alias"] = "test-account-\(index)"
      accounts.append(account)
    }
    root["accounts"] = accounts
    let data = try JSONSerialization.data(withJSONObject: root)
    let (model, _, _, _) = makeModel([
      .init(command: .status, output: success(data))
    ])
    await model.menuOpened()
    let host = NSHostingView(rootView: SwitchMenu(model: model))
    // The two-account menu previously fitted into ~239 points, leaving only
    // ~22 points for the scroll area and clipping both quota and the next row.
    #expect(host.fittingSize.height >= 360)
    // Long lists must scroll without pushing the menu actions out of view.
    #expect(host.fittingSize.height <= 640)
  }
  @Test func inFlightProgressDoesNotResizeMenu() async throws {
    let gate = ExecutionGate()
    let (model, _, _, _) = makeModel([
      .init(command: .status, output: success(statusData())),
      .init(command: .refreshUsage, output: success(try refreshedAccounts()), gate: gate),
      .init(command: .status, output: success(statusData())),
    ])
    await model.menuOpened()
    let host = NSHostingView(rootView: SwitchMenu(model: model))
    let height = host.fittingSize.height
    let refreshing = Task { await model.refreshUsage() }
    await gate.waitForEntry()
    #expect(model.isBusy)
    #expect(abs(host.fittingSize.height - height) <= 1)
    await gate.resume()
    await refreshing.value
  }

}
