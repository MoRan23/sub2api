import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/vue";
import GroupApplicationCommunicationTimeline from "./GroupApplicationCommunicationTimeline.vue";

vi.mock("vue-i18n", async (importOriginal) => {
  const actual = await importOriginal<typeof import("vue-i18n")>();
  const translations: Record<string, string> = {
    "common.retry": "Retry",
    "common.loading": "Loading",
    "admin.groupApplications.emailHistory": "Email conversation history",
    "admin.groupApplications.refreshEmailHistory": "Refresh email conversation history",
    "admin.groupApplications.exportEmailHistory": "Export email conversation history",
    "admin.groupApplications.noEmailHistory": "No email history",
    "admin.groupApplications.outboundEmail": "Sent",
    "admin.groupApplications.inboundEmail": "Received",
    "admin.groupApplications.to": "To",
    "admin.groupApplications.from": "From",
    "admin.groupApplications.attachment": "Attachment",
    "admin.groupApplications.attempts": "1 attempt",
    "admin.groupApplications.templateKinds.approval": "Approval awaiting reply",
    "admin.groupApplications.communicationResults.completed": "Confirmation matched and completed",
    "admin.groupApplications.emailContentUnavailable": "Content unavailable",
    "admin.groupApplications.emailContentTruncated": "Content truncated",
  };
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => translations[key] ?? key }),
  };
});

const communications = [
  {
    id: 1,
    application_id: 7,
    direction: "outbound" as const,
    kind: "approval" as const,
    to_address: "applicant@example.com",
    subject: "Application approved",
    html_body: '<p>Read the agreement</p><img src="x" onerror="alert(1)">',
    status: "failed",
    attempts: 1,
    occurred_at: "2026-08-27T01:00:00Z",
  },
  {
    id: 2,
    application_id: 7,
    direction: "inbound" as const,
    result: "completed",
    from_address: "applicant@example.com",
    subject: "Re: Application approved",
    text_body: "I ACCEPT",
    occurred_at: "2026-08-27T01:05:00Z",
  },
];

describe("GroupApplicationCommunicationTimeline", () => {
  it("shows both directions, sanitizes HTML, and exposes export and retry commands", async () => {
    const onExport = vi.fn();
    const onRefresh = vi.fn();
    const onRetry = vi.fn();
    const { container } = render(GroupApplicationCommunicationTimeline, {
      props: { communications, onExport, onRefresh, onRetry },
    });

    expect(screen.getByText("Application approved")).toBeTruthy();
    expect(screen.getByText("I ACCEPT")).toBeTruthy();
    expect(screen.getByText("Confirmation matched and completed")).toBeTruthy();
    expect(container.innerHTML).not.toContain("onerror");
    expect(container.querySelector("img")).toBeNull();

    await fireEvent.click(
      screen.getByRole("button", { name: "Refresh email conversation history" }),
    );
    await fireEvent.click(
      screen.getByRole("button", { name: "Export email conversation history" }),
    );
    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(onRefresh).toHaveBeenCalledTimes(1);
    expect(onExport).toHaveBeenCalledTimes(1);
    expect(onRetry).toHaveBeenCalledWith(1);
  });
});
