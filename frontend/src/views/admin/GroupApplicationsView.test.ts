import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/vue";
import { createPinia } from "pinia";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import GroupApplicationsView from "./GroupApplicationsView.vue";

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
    "admin.groupApplications.allStatuses": "All statuses",
    "admin.groupApplications.search": "Search",
    "admin.groupApplications.noApplications": "No applications",
    "admin.groupApplications.user": "User",
    "admin.groupApplications.email": "Email",
    "common.refresh": "Refresh",
    "common.loading": "Loading",
    "common.enabled": "Enabled",
    "common.disabled": "Disabled",
    "common.status": "Status",
    "common.createdAt": "Created",
    "common.saved": "Saved",
    "common.error": "Error",
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

const initialConfig = {
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

const server = setupServer(
  http.get("*/api/v1/admin/group-applications/email-config", () =>
    HttpResponse.json({ code: 0, data: initialConfig }),
  ),
  http.get("*/api/v1/admin/group-applications/worker-status", () =>
    HttpResponse.json({
      code: 0,
      data: {
        running: true,
        workflow_enabled: false,
        mail_processed: 0,
        mail_failures: 0,
        replies_processed: 0,
        reply_failures: 0,
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
    return HttpResponse.json({ code: 0, data: savedConfig });
  }),
);

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(() => {
  testedSMTP = null;
  testedIMAP = null;
  savedConfig = null;
  server.resetHandlers();
});
afterAll(() => server.close());

describe("GroupApplicationsView email settings", () => {
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
  });
});
