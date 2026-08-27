import { describe, expect, it } from "vitest";

import en from "../locales/en/admin/groupApplications";
import zh from "../locales/zh/admin/groupApplications";

const linkedInboundResults = [
  "completed",
  "reply_mismatch",
  "ignored_sender",
  "automated",
  "unsupported_content",
  "disabled",
  "state_conflict",
  "not_found",
  "unavailable",
  "error",
] as const;

describe.each([
  ["en", en],
  ["zh", zh],
] as const)("%s group application messages", (_locale, messages) => {
  it("labels every linked inbound processing result", () => {
    const results = messages.groupApplications.communicationResults;

    for (const result of linkedInboundResults) {
      expect(results[result]).toBeTruthy();
    }
  });
});
