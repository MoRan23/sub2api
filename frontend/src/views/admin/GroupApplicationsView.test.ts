import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/vue";
import { createPinia } from "pinia";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import GroupApplicationsView from "./GroupApplicationsView.vue";
import { apiClient } from "@/api/client";
import {
  defaultGroupApplicationTemplates,
  groupApplicationsAdminAPI,
  type AdminGroupApplication,
  type GroupApplicationCommunication,
  type GroupApplicationEmailConfig,
  type GroupApplicationPolicy,
} from "@/api/admin/groupApplications";

vi.mock("vue-i18n", async (importOriginal) => {
  const actual = await importOriginal<typeof import("vue-i18n")>();
  const translations: Record<string, string> = {
    "admin.groupApplications.title": "Group Applications",
    "admin.groupApplications.description": "Manage application email workflows",
    "admin.groupApplications.applications": "Applications",
    "admin.groupApplications.policies": "Policies",
    "admin.groupApplications.emailSettings": "Email settings",
    "admin.groupApplications.emailModule": "Group application email module",
    "admin.groupApplications.emailModuleHint": "Standalone settings only",
    "admin.groupApplications.smtpTitle": "SMTP delivery",
    "admin.groupApplications.smtpHint": "Send workflow messages",
    "admin.groupApplications.imapTitle": "IMAP replies",
    "admin.groupApplications.imapHint": "Read replies",
    "admin.groupApplications.username": "Username",
    "admin.groupApplications.password": "Password",
    "admin.groupApplications.senderAddress": "Sender address",
    "admin.groupApplications.senderName": "Sender name",
    "admin.groupApplications.connectionEncryption": "Connection encryption",
    "admin.groupApplications.smtpEncryption": "SMTP connection encryption",
    "admin.groupApplications.imapEncryption": "IMAP connection encryption",
    "admin.groupApplications.implicitTLS": "Implicit TLS",
    "admin.groupApplications.implicitTLSHint": "Implicit {port}",
    "admin.groupApplications.startTLSHint": "STARTTLS {port}",
    "admin.groupApplications.passwordRequiredHint": "Password required",
    "admin.groupApplications.passwordConfiguredHint": "Password saved",
    "admin.groupApplications.testRecipient": "Test message recipient",
    "admin.groupApplications.testSMTP": "Test SMTP connection",
    "admin.groupApplications.sendTest": "Send test message",
    "admin.groupApplications.testIMAP": "Test IMAP and list folders",
    "admin.groupApplications.smtpConnectionOK": "SMTP succeeded",
    "admin.groupApplications.imapConnectionOK": "IMAP succeeded",
    "admin.groupApplications.testEmailSent": "Test sent",
    "admin.groupApplications.reuseSMTPCredentials": "Reuse SMTP username and password",
    "admin.groupApplications.reuseSMTPCredentialsHint": "Reuse credentials",
    "admin.groupApplications.replyAddress": "Reply address",
    "admin.groupApplications.mailbox": "Mailbox",
    "admin.groupApplications.mailboxPlaceholder": "Select mailbox",
    "admin.groupApplications.mailboxHint": "Test to discover folders",
    "admin.groupApplications.pollInterval": "Poll interval",
    "admin.groupApplications.saveEmailConfig": "Save email settings",
    "admin.groupApplications.workflowPaused": "Workflow paused",
    "admin.groupApplications.workerStopped": "Worker stopped",
    "admin.groupApplications.workflowRunning": "Workflow running",
    "admin.groupApplications.lastMailError": "Latest outbox error: {error}",
    "admin.groupApplications.lastIMAPError": "Latest IMAP error: {error}",
    "admin.groupApplications.applyableGroup": "Application group",
    "admin.groupApplications.policyDescription": "Configure access policy",
    "admin.groupApplications.enabled": "Allow applications",
    "admin.groupApplications.replyPhrase": "Strict reply phrase",
    "admin.groupApplications.replyPhraseHint": "Must match exactly",
    "admin.groupApplications.pdfAgreement": "Agreement PDF",
    "admin.groupApplications.choosePDF": "Choose PDF",
    "admin.groupApplications.downloadAgreement": "Download agreement",
    "admin.groupApplications.noPDF": "No PDF selected",
    "admin.groupApplications.mailTemplates": "Email templates",
    "admin.groupApplications.templateHint": "Per-group templates",
    "admin.groupApplications.templateType": "Template type",
    "admin.groupApplications.subject": "Subject",
    "admin.groupApplications.subjectPlaceholder": "Enter subject",
    "admin.groupApplications.templateKinds.approval": "Approval",
    "admin.groupApplications.templateKinds.completion": "Completion",
    "admin.groupApplications.templateKinds.manual_rejection": "Manual rejection",
    "admin.groupApplications.templateKinds.reply_mismatch": "Reply mismatch",
    "admin.groupApplications.templateKinds.revocation": "Revocation",
    "admin.groupApplications.allStatuses": "All statuses",
    "admin.groupApplications.search": "Search",
    "admin.groupApplications.noApplications": "No applications",
    "admin.groupApplications.user": "User",
    "admin.groupApplications.email": "Email",
    "admin.groupApplications.resendApproval": "Resend approval",
    "admin.groupApplications.approvalAlreadyQueued": "Approval already queued",
    "admin.groupApplications.emailHistory": "Email conversation history",
    "admin.groupApplications.refreshEmailHistory": "Refresh email conversation history",
    "admin.groupApplications.exportEmailHistory": "Export email conversation history",
    "admin.groupApplications.outboundEmail": "Outbound",
    "admin.groupApplications.to": "To",
    "admin.groupApplications.attempts": "{count} attempts",
    "groupApplications.contactEmail": "Contact email",
    "groupApplications.reason": "Reason",
    "groupApplications.group": "Group",
    "groupApplications.status.awaiting_reply": "Awaiting email reply",
    "common.refresh": "Refresh",
    "common.loading": "Loading",
    "common.enabled": "Enabled",
    "common.disabled": "Disabled",
    "common.status": "Status",
    "common.createdAt": "Created",
    "common.save": "Save",
    "common.saved": "Saved",
    "common.error": "Error",
    "common.close": "Close",
    "groupApplications.selectGroup": "Select group",
  };
  return {
    ...actual,
    useI18n: () => ({
      locale: { value: "en" },
      t: (key: string, params?: Record<string, unknown>) => {
        let value = translations[key] ?? key;
        for (const [name, replacement] of Object.entries(params ?? {})) {
          value = value.replace(`{${name}}`, String(replacement));
        }
        return value;
      },
    }),
  };
});

const initialConfig: GroupApplicationEmailConfig = {
  enabled: false,
  smtp: {
    host: "smtp.old.example.com",
    port: 587,
    username: "sender@example.com",
    password_configured: true,
    from_address: "sender@example.com",
    from_name: "Applications",
    tls_mode: "starttls",
  },
  imap: {
    host: "imap.example.com",
    port: 993,
    username: "inbox@example.com",
    password_configured: true,
    use_smtp_credentials: false,
    mailbox: "INBOX",
    reply_address: "reply@example.com",
    tls_mode: "implicit",
    poll_interval_seconds: 60,
  },
};

let testedSMTP: typeof initialConfig | null = null;
let testedIMAP: typeof initialConfig | null = null;
let savedConfig: typeof initialConfig | null = null;
let workerWorkflowEnabled = false;
let workerLastMailError = "";
let workerLastIMAPError = "";

const server = setupServer(
  http.get("*/api/v1/admin/group-applications/email-config", () =>
    HttpResponse.json({ code: 0, data: savedConfig ?? initialConfig }),
  ),
  http.get("*/api/v1/admin/group-applications/worker-status", () =>
    HttpResponse.json({
      code: 0,
      data: {
        running: true,
        workflow_enabled: workerWorkflowEnabled,
        mail_processed: 0,
        mail_failures: 0,
        replies_processed: 0,
        reply_failures: 0,
        last_mail_error: workerLastMailError || undefined,
        last_imap_error: workerLastIMAPError || undefined,
      },
    }),
  ),
  http.get("*/api/v1/admin/group-applications", () =>
    HttpResponse.json({ code: 0, data: { items: [], total: 0 } }),
  ),
  http.get("*/api/v1/admin/group-application-policies", () =>
    HttpResponse.json({ code: 0, data: [] }),
  ),
  http.get("*/api/v1/admin/groups/all", () =>
    HttpResponse.json({ code: 0, data: [] }),
  ),
  http.post("*/api/v1/admin/group-applications/email-config/test-smtp", async ({ request }) => {
    testedSMTP = (await request.json()) as typeof initialConfig;
    return HttpResponse.json({ code: 0, data: { ok: true } });
  }),
  http.post("*/api/v1/admin/group-applications/email-config/test-imap", async ({ request }) => {
    testedIMAP = (await request.json()) as typeof initialConfig;
    return HttpResponse.json({
      code: 0,
      data: { ok: true, mailboxes: ["INBOX", "Archive/Applications"] },
    });
  }),
  http.put("*/api/v1/admin/group-applications/email-config", async ({ request }) => {
    savedConfig = (await request.json()) as typeof initialConfig;
    workerWorkflowEnabled = savedConfig.enabled;
    return HttpResponse.json({ code: 0, data: savedConfig });
  }),
);

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(() => {
  testedSMTP = null;
    testedIMAP = null;
  savedConfig = null;
  workerWorkflowEnabled = false;
  workerLastMailError = "";
  workerLastIMAPError = "";
  vi.useRealTimers();
  vi.restoreAllMocks();
  server.resetHandlers();
});
afterAll(() => server.close());

describe("GroupApplicationsView email settings", () => {
  it("bounds the IMAP test request independently from the global client timeout", async () => {
    const post = vi.spyOn(apiClient, "post").mockResolvedValue({
      data: { ok: true, mailboxes: ["INBOX"] },
    });

    await groupApplicationsAdminAPI.testIMAP(initialConfig);

    expect(post).toHaveBeenCalledWith(
      "/admin/group-applications/email-config/test-imap",
      initialConfig,
      { timeout: 15_000 },
    );
  });

  it("tests and saves the unsaved standalone configuration and discovers mailboxes", async () => {
    render(GroupApplicationsView, {
      global: {
        plugins: [createPinia()],
        stubs: { AppLayout: { template: "<main><slot /></main>" } },
      },
    });

    await fireEvent.click(await screen.findByRole("tab", { name: "Email settings" }));
    expect(await screen.findByText("SMTP delivery")).toBeTruthy();

    const hosts = screen.getAllByLabelText("Host");
    await fireEvent.update(hosts[0], "smtp.unsaved.example.com");
    await fireEvent.click(screen.getByRole("button", { name: "SMTP connection encryption" }));
    await fireEvent.click(await screen.findByRole("option", { name: "Implicit TLS" }));
    expect((screen.getAllByLabelText("Port")[0] as HTMLInputElement).value).toBe("465");

    await fireEvent.click(screen.getByRole("button", { name: "Test SMTP connection" }));
    await waitFor(() => expect(testedSMTP?.smtp.host).toBe("smtp.unsaved.example.com"));
    expect(testedSMTP?.smtp.tls_mode).toBe("implicit");

    await fireEvent.click(screen.getByRole("button", { name: "Test IMAP and list folders" }));
    await waitFor(() => expect(testedIMAP?.imap.mailbox).toBe("INBOX"));
    await fireEvent.click(screen.getByRole("button", { name: "Mailbox" }));
    expect(await screen.findByRole("option", { name: "Archive/Applications" })).toBeTruthy();

    await fireEvent.click(screen.getAllByRole("switch")[0]);
    await fireEvent.click(screen.getByRole("button", { name: "Save email settings" }));
    await waitFor(() => expect(savedConfig?.enabled).toBe(true));
    expect(savedConfig?.smtp.host).toBe("smtp.unsaved.example.com");
    expect(savedConfig?.imap.host).toBe("imap.example.com");
    expect(await screen.findByText("Workflow running")).toBeTruthy();
  });

  it("shows independent outbox and IMAP worker failures", async () => {
    workerLastMailError = "SMTP authentication failed";
    workerLastIMAPError = "IMAP connection timed out";

    render(GroupApplicationsView, {
      global: {
        plugins: [createPinia()],
        stubs: { AppLayout: { template: "<main><slot /></main>" } },
      },
    });

    await fireEvent.click(await screen.findByRole("tab", { name: "Email settings" }));
    expect(await screen.findByText("Latest outbox error: SMTP authentication failed")).toBeTruthy();
    expect(screen.getByText("Latest IMAP error: IMAP connection timed out")).toBeTruthy();
  });
});

describe("GroupApplicationsView communication refresh", () => {
  const application: AdminGroupApplication = {
    id: 7,
    user_id: 42,
    user_email: "applicant@example.com",
    group_id: 4,
    group_name: "Private Pro",
    contact_email: "applicant@example.com",
    reason: "Need private access",
    locale: "en",
    status: "awaiting_reply",
    attachment_id: 11,
    created_at: "2026-08-27T01:00:00Z",
    updated_at: "2026-08-27T01:00:00Z",
  };

  function approvalCommunication(status: string): GroupApplicationCommunication {
    return {
      id: 11,
      application_id: application.id,
      direction: "outbound",
      kind: "approval",
      to_address: application.contact_email,
      subject: "Application approved",
      status,
      attempts: 0,
      occurred_at: "2026-08-27T01:01:00Z",
    };
  }

  it("silently refreshes queued mail without overlapping and stops after delivery", async () => {
    savedConfig = { ...initialConfig, enabled: true };
    workerWorkflowEnabled = true;
    server.use(
      http.get("*/api/v1/admin/group-applications", () =>
        HttpResponse.json({ code: 0, data: { items: [application], total: 1 } }),
      ),
    );
    const getApplication = vi
      .spyOn(groupApplicationsAdminAPI, "get")
      .mockResolvedValue(application);
    const listCommunications = vi
      .spyOn(groupApplicationsAdminAPI, "listCommunications")
      .mockResolvedValueOnce([approvalCommunication("pending")])
      .mockResolvedValue([approvalCommunication("sent")]);

    render(GroupApplicationsView, {
      global: {
        plugins: [createPinia()],
        stubs: { AppLayout: { template: "<main><slot /></main>" } },
      },
    });

    const applicationID = await screen.findByText("#7");
    vi.useFakeTimers();
    await fireEvent.click(applicationID);
    await vi.advanceTimersByTimeAsync(0);

    const resend = screen.getByRole("button", { name: "Resend approval" });
    expect((resend as HTMLButtonElement).disabled).toBe(true);
    expect(resend.getAttribute("title")).toBe("Approval already queued");
    expect(listCommunications).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(3000);
    expect(getApplication).toHaveBeenCalledTimes(2);
    expect(listCommunications).toHaveBeenCalledTimes(2);
    expect((resend as HTMLButtonElement).disabled).toBe(false);

    await vi.advanceTimersByTimeAsync(6000);
    expect(listCommunications).toHaveBeenCalledTimes(2);
  });

  it("cancels queued refreshes when the details dialog closes", async () => {
    server.use(
      http.get("*/api/v1/admin/group-applications", () =>
        HttpResponse.json({ code: 0, data: { items: [application], total: 1 } }),
      ),
    );
    vi.spyOn(groupApplicationsAdminAPI, "get").mockResolvedValue(application);
    const listCommunications = vi
      .spyOn(groupApplicationsAdminAPI, "listCommunications")
      .mockResolvedValue([approvalCommunication("pending")]);

    render(GroupApplicationsView, {
      global: {
        plugins: [createPinia()],
        stubs: { AppLayout: { template: "<main><slot /></main>" } },
      },
    });

    const applicationID = await screen.findByText("#7");
    vi.useFakeTimers();
    await fireEvent.click(applicationID);
    await vi.advanceTimersByTimeAsync(0);
    expect(listCommunications).toHaveBeenCalledTimes(1);

    await fireEvent.click(screen.getByRole("button", { name: "Close" }));
    await vi.advanceTimersByTimeAsync(6000);
    expect(listCommunications).toHaveBeenCalledTimes(1);
  });
});

describe("GroupApplicationsView policies", () => {
  it("uploads the agreement as multipart and preserves saved policy fields when reselected", async () => {
    const policy: GroupApplicationPolicy = {
      group_id: 4,
      group_name: "Private Pro",
      enabled: true,
      reply_phrase: "EXACT-CONFIRM",
      templates: defaultGroupApplicationTemplates(),
      attachment_id: 11,
      attachment_name: "agreement-v1.pdf",
      attachment_size: 2048,
    };
    let uploadedAttachment: File | undefined;
    let submittedPolicy: GroupApplicationPolicy | null = null;

    server.use(
      http.get("*/api/v1/admin/group-application-policies", () =>
        HttpResponse.json({ code: 0, data: [policy] }),
      ),
      http.get("*/api/v1/admin/groups/all", () =>
        HttpResponse.json({
          code: 0,
          data: [
            {
              id: 4,
              name: "Private Pro",
              is_exclusive: true,
              subscription_type: "standard",
              status: "active",
            },
          ],
        }),
      ),
    );
    vi.spyOn(groupApplicationsAdminAPI, "savePolicy").mockImplementation(
      async (groupID, submitted, uploaded) => {
        submittedPolicy = submitted;
        uploadedAttachment = uploaded;
        return {
          ...submitted,
          group_id: groupID,
          group_name: "Private Pro",
          attachment_id: 12,
          attachment_name: uploaded?.name,
          attachment_size: uploaded?.size,
        };
      },
    );

    const { container } = render(GroupApplicationsView, {
      global: {
        plugins: [createPinia()],
        stubs: { AppLayout: { template: "<main><slot /></main>" } },
      },
    });

    await fireEvent.click(await screen.findByRole("tab", { name: "Policies" }));
    const replyPhrase = await screen.findByLabelText("Strict reply phrase");
    expect((replyPhrase as HTMLInputElement).value).toBe("EXACT-CONFIRM");
    expect(screen.getByText("agreement-v1.pdf")).toBeTruthy();
    expect(screen.getByRole("switch").getAttribute("aria-checked")).toBe("true");

    const groupButtons = screen.getAllByRole("button", { name: "Private Pro" });
    await fireEvent.click(groupButtons[groupButtons.length - 1]);
    expect((replyPhrase as HTMLInputElement).value).toBe("EXACT-CONFIRM");
    expect(screen.getByRole("switch").getAttribute("aria-checked")).toBe("true");

    const file = new window.File(["%PDF-1.7\nfixture"], "agreement-v2.pdf", {
      type: "application/pdf",
    });
    const fileInput = container.querySelector<HTMLInputElement>('input[type="file"]');
    expect(fileInput).not.toBeNull();
    Object.defineProperty(fileInput!, "files", {
      configurable: true,
      value: [file],
    });
    await fireEvent.change(fileInput!);
    expect(await screen.findByText("agreement-v2.pdf")).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(uploadedAttachment?.name).toBe("agreement-v2.pdf"));
    expect(submittedPolicy?.enabled).toBe(true);
    expect(submittedPolicy?.reply_phrase).toBe("EXACT-CONFIRM");
    expect(await screen.findByText("agreement-v2.pdf")).toBeTruthy();

    const savedGroupButtons = screen.getAllByRole("button", { name: "Private Pro" });
    await fireEvent.click(savedGroupButtons[savedGroupButtons.length - 1]);
    expect((screen.getByLabelText("Strict reply phrase") as HTMLInputElement).value).toBe(
      "EXACT-CONFIRM",
    );
    expect(screen.getByRole("switch").getAttribute("aria-checked")).toBe("true");
  });
});
