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
    "admin.groupApplications.replyStatuses.completed": "Reply workflow completed",
    "admin.groupApplications.templateKinds.approval": "Approval awaiting reply",
    "admin.groupApplications.communicationResults.completed": "Confirmation matched and completed",
    "admin.groupApplications.emailContentUnavailable": "Content unavailable",
    "admin.groupApplications.emailContentTruncated": "Content truncated",
    "admin.groupApplications.emailPreview": "Email HTML body preview",
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
    html_body: `<!doctype html>
      <html>
        <head><style>.message { color: #0f766e; }</style></head>
        <body style="margin: 0; background: #f3f4f6;">
          <p class="message" style="font-weight: 700;">Read the agreement</p>
          <a href="https://attacker.example/track">unsafe link</a>
          <img src="https://attacker.example/pixel.png" onerror="alert(1)">
          <form action="https://attacker.example/submit"><button>Submit</button></form>
          <script>alert(1)</script>
        </body>
      </html>`,
    status: "failed",
    retryable: true,
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
  it("shows both directions, isolates sanitized HTML, and exposes export and retry commands", async () => {
    const onExport = vi.fn();
    const onRefresh = vi.fn();
    const onRetry = vi.fn();
    const { container } = render(GroupApplicationCommunicationTimeline, {
      props: { communications, onExport, onRefresh, onRetry },
    });

    expect(screen.getByText("Application approved")).toBeTruthy();
    expect(screen.getByText("I ACCEPT")).toBeTruthy();
    expect(screen.getByText("Confirmation matched and completed")).toBeTruthy();
    const preview = screen.getByTitle("Email HTML body preview") as HTMLIFrameElement;
    const sourceDocument = preview.srcdoc;
    expect(preview.getAttribute("sandbox")).toBe("");
    expect(preview.getAttribute("referrerpolicy")).toBe("no-referrer");
    expect(sourceDocument).toContain("Content-Security-Policy");
    expect(sourceDocument).toContain("default-src 'none'");
    expect(sourceDocument).toContain("<style>");
    expect(sourceDocument).toContain('style="margin: 0; background: #f3f4f6;"');
    expect(sourceDocument).not.toContain("onerror");
    expect(sourceDocument).not.toContain("<script");
    expect(sourceDocument).not.toContain("<form");
    expect(sourceDocument).not.toContain("attacker.example");
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

  it("allows long sender addresses and subjects to wrap within the timeline", () => {
    const longSender = `${"very-long-local-part".repeat(6)}@example.com`;
    const longSubject = "A-subject-without-natural-breaks".repeat(8);
    render(GroupApplicationCommunicationTimeline, {
      props: {
        communications: [
          {
            ...communications[1],
            from_address: longSender,
            subject: longSubject,
          },
        ],
      },
    });

    const sender = screen.getByText(
      (_content, element) =>
        element?.tagName === "SPAN" && element.textContent?.includes(longSender) === true,
    );
    expect(sender.closest(".break-all")).toBeTruthy();
    expect(screen.getByText(longSubject).classList.contains("break-all")).toBe(true);
  });

  it("keeps the delivery result but closes retry after the reply workflow completes", () => {
    render(GroupApplicationCommunicationTimeline, {
      props: {
        communications: [
          {
            ...communications[0],
            reply_status: "completed" as const,
            retryable: false,
          },
        ],
      },
    });

    expect(screen.getByText(/failed.*Reply workflow completed/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
  });

  it("hides retry for a failed mail whose required application state has ended", () => {
    render(GroupApplicationCommunicationTimeline, {
      props: {
        communications: [
          {
            ...communications[0],
            kind: "completion" as const,
            retryable: false,
          },
        ],
      },
    });

    expect(screen.getByText(/failed/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
  });
});
