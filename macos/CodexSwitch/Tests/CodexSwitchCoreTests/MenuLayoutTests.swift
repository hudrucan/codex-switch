import AppKit
import Foundation
import SwiftUI
import Testing

@testable import CodexSwitchApp

@Suite @MainActor
struct MenuLayoutTests {
  @Test(arguments: [2, 8])
  func accountListHasRoomForVisibleQuotaRows(accountCount: Int) async throws {
    var root = try #require(JSONSerialization.jsonObject(with: freshStatusData()) as? [String: Any])
    var accounts = try #require(root["accounts"] as? [[String: Any]])
    accounts[1]["usage"] = accounts[0]["usage"]
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
  @Test(arguments: [false, true])
  func inFlightProgressDoesNotResizeMenu(switching: Bool) async throws {
    let gate = ExecutionGate()
    var steps: [ScriptedExecutor.Step] = [
      .init(command: .status, output: success(freshStatusData()))
    ]
    if switching {
      steps += [
        .init(command: .status, output: success(freshStatusData())),
        .init(command: .use("personal"), output: switchOutput, gate: gate),
        .init(command: .status, output: success(freshStatusData(active: "b"))),
      ]
    } else {
      steps += [
        .init(command: .refreshUsage("work"), output: success(try refreshedAccount()), gate: gate),
        .init(
          command: .refreshUsage("personal"),
          output: success(try refreshedAccount(alias: "personal"))),
        .init(command: .status, output: success(freshStatusData())),
      ]
    }
    let (model, _, _, _) = makeModel(steps)
    await model.menuOpened()
    let host = NSHostingView(rootView: SwitchMenu(model: model))
    let height = host.fittingSize.height
    let operation = Task {
      if switching {
        await model.beginSwitch(accountID: "b")
      } else {
        await model.refreshUsage()
      }
    }
    await gate.waitForEntry()
    #expect(model.isBusy)
    #expect(abs(host.fittingSize.height - height) <= 1)
    await gate.resume()
    await operation.value
  }

  @Test func manyProcessDetailsStayCollapsedAndDoNotGrowConfirmation() async {
    let processes =
      "["
      + (4242..<4262).map { pid in
        "{\"pid\":\(pid),\"command\":\"Codex\"}"
      }.joined(separator: ",") + "]"
    let (model, _, _, _) = makeModel([
      .init(command: .status, output: success(freshStatusData(processes: processes)))
    ])
    await model.beginSwitch(accountID: "b")
    #expect(model.phase == .awaitingQuit)
    let host = NSHostingView(rootView: SwitchMenu(model: model))
    // Extra process count must not add an unbounded row for each PID.
    #expect(host.fittingSize.height < 760)
  }

}
