import { apiClient } from "../client";
import type {
  GroupApplication,
  GroupApplicationStatus,
} from "../groupApplications";

export type GroupApplicationMailKind =
  | "approval"
  | "completion"
  | "manual_rejection"
  | "reply_mismatch"
  | "revocation";

export interface LocalizedMailTemplate {
  subject: string;
  html: string;
}
export type GroupApplicationTemplateSet = Record<
  GroupApplicationMailKind,
  Record<"zh" | "en", LocalizedMailTemplate>
>;

export interface GroupApplicationPolicy {
  group_id: number;
  group_name: string;
  enabled: boolean;
  reply_phrase: string;
  templates: GroupApplicationTemplateSet;
  attachment_id?: number;
  attachment_name?: string;
  attachment_size?: number;
  attachment_sha256?: string;
  created_at?: string;
  updated_at?: string;
}

export interface GroupApplicationMailStatus {
  id: number;
  kind: GroupApplicationMailKind;
  message_id: string;
  status: string;
  reply_status?: "awaiting_reply" | "completed";
  delivery_active?: boolean;
  retryable?: boolean;
  attempts: number;
  last_error?: string;
  sent_at?: string;
  created_at: string;
}

export interface GroupApplicationCommunication {
  id: number;
  application_id: number;
  direction: "outbound" | "inbound";
  kind?: GroupApplicationMailKind;
  result?: string;
  from_address?: string;
  to_address?: string;
  subject?: string;
  html_body?: string;
  text_body?: string;
  content_unavailable?: boolean;
  content_truncated?: boolean;
  message_id?: string;
  in_reply_to?: string;
  references?: string;
  reply_sha256?: string;
  attachment_id?: number;
  attachment_name?: string;
  attachment_size?: number;
  status?: string;
  reply_status?: "awaiting_reply" | "completed";
  delivery_active?: boolean;
  retryable?: boolean;
  attempts?: number;
  last_error?: string;
  sent_at?: string;
  occurred_at: string;
}

export interface AdminGroupApplication extends GroupApplication {
  mails?: GroupApplicationMailStatus[];
}
export interface GroupApplicationListResult {
  items: AdminGroupApplication[];
  total: number;
}

export type GroupApplicationTLSMode = "implicit" | "starttls";

export interface GroupApplicationSMTPConfig {
  host: string;
  port: number;
  username: string;
  password?: string;
  password_configured: boolean;
  from_address: string;
  from_name: string;
  tls_mode: GroupApplicationTLSMode;
}

export interface GroupApplicationIMAPConfig {
  host: string;
  port: number;
  username: string;
  password?: string;
  password_configured: boolean;
  use_smtp_credentials: boolean;
  mailbox: string;
  reply_address: string;
  tls_mode: GroupApplicationTLSMode;
  poll_interval_seconds: number;
}

export interface GroupApplicationEmailConfig {
  enabled: boolean;
  smtp: GroupApplicationSMTPConfig;
  imap: GroupApplicationIMAPConfig;
  legacy_imported?: boolean;
}

export interface GroupApplicationWorkerHealth {
  running: boolean;
  workflow_enabled: boolean;
  mail_processed: number;
  mail_failures: number;
  replies_processed: number;
  reply_failures: number;
  last_mail_check_at?: string;
  last_mail_error?: string;
  last_imap_check_at?: string;
  last_imap_error?: string;
  configuration_error?: string;
}

function groupApplicationEmailCard(
  accent: string,
  eyebrow: string,
  title: string,
  content: string,
  footer: string,
): string {
  return `<!doctype html>
<html>
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>${title}</title>
</head>
<body style="margin:0;padding:0;background:#f3f4f6;color:#17202a;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',Arial,sans-serif;">
  <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="width:100%;border-collapse:collapse;background:#f3f4f6;">
    <tr>
      <td align="center" style="padding:32px 16px;">
        <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="width:100%;max-width:640px;border-collapse:separate;background:#ffffff;border:1px solid #e5e7eb;border-radius:8px;overflow:hidden;box-shadow:0 10px 28px rgba(15,23,42,0.08);">
          <tr><td style="height:8px;background:${accent};font-size:0;line-height:0;">&nbsp;</td></tr>
          <tr>
            <td style="padding:36px 40px 28px;">
              <p style="margin:0 0 10px;color:${accent};font-size:12px;font-weight:700;letter-spacing:0;text-transform:uppercase;">${eyebrow}</p>
              <h1 style="margin:0 0 24px;color:#111827;font-size:28px;line-height:1.3;font-weight:700;">${title}</h1>
              ${content}
            </td>
          </tr>
          <tr>
            <td style="padding:18px 40px;background:#f9fafb;border-top:1px solid #e5e7eb;">
              <p style="margin:0;color:#6b7280;font-size:12px;line-height:1.7;">${footer}</p>
            </td>
          </tr>
        </table>
      </td>
    </tr>
  </table>
</body>
</html>`;
}

export function defaultGroupApplicationTemplates(): GroupApplicationTemplateSet {
  return {
    approval: {
      zh: {
        subject: "{{site_name}} - {{group_name}} 分组申请已批准",
        html: groupApplicationEmailCard(
          "#0f766e",
          "GROUP ACCESS / 分组申请",
          "申请已通过初审",
          `<p style="margin:0 0 14px;font-size:16px;line-height:1.8;">您好，</p>
              <p style="margin:0 0 22px;font-size:16px;line-height:1.8;">您的 <strong>{{group_name}}</strong> 分组申请已获批准。正式开放访问权限前，请完成以下确认步骤。</p>
              <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="width:100%;border-collapse:separate;background:#ecfdf5;border:1px solid #a7f3d0;border-radius:8px;">
                <tr><td style="padding:18px 20px;color:#065f46;font-size:14px;line-height:1.8;"><strong>1.</strong> 阅读本邮件附件 <strong>{{attachment_name}}</strong><br><strong>2.</strong> 使用当前邮箱直接回复本邮件<br><strong>3.</strong> 回复正文只能包含下方确认词，不得附加签名或其他文字</td></tr>
              </table>
              <p style="margin:24px 0 10px;color:#4b5563;font-size:13px;font-weight:600;">严格回复词</p>
              <div style="padding:16px 20px;background:#111827;color:#ffffff;border-radius:8px;font-family:ui-monospace,SFMono-Regular,Consolas,'Liberation Mono',monospace;font-size:18px;font-weight:700;line-height:1.5;text-align:center;overflow-wrap:anywhere;">{{reply_phrase}}</div>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">申请编号：#{{application_id}} · 提交时间：{{submitted_at}}</p>`,
          "请直接回复本邮件完成确认。本邮件由 {{site_name}} 的分组申请系统发送。",
        ),
      },
      en: {
        subject: "{{site_name}} - {{group_name}} application approved",
        html: groupApplicationEmailCard(
          "#0f766e",
          "GROUP ACCESS",
          "Application approved for confirmation",
          `<p style="margin:0 0 14px;font-size:16px;line-height:1.8;">Hello,</p>
              <p style="margin:0 0 22px;font-size:16px;line-height:1.8;">Your application for <strong>{{group_name}}</strong> has been approved. Complete the confirmation below before access is enabled.</p>
              <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="width:100%;border-collapse:separate;background:#ecfdf5;border:1px solid #a7f3d0;border-radius:8px;">
                <tr><td style="padding:18px 20px;color:#065f46;font-size:14px;line-height:1.8;"><strong>1.</strong> Read the attached agreement <strong>{{attachment_name}}</strong><br><strong>2.</strong> Reply directly from this email address<br><strong>3.</strong> Put only the confirmation phrase below in the reply body, with no signature or other text</td></tr>
              </table>
              <p style="margin:24px 0 10px;color:#4b5563;font-size:13px;font-weight:600;">Exact confirmation phrase</p>
              <div style="padding:16px 20px;background:#111827;color:#ffffff;border-radius:8px;font-family:ui-monospace,SFMono-Regular,Consolas,'Liberation Mono',monospace;font-size:18px;font-weight:700;line-height:1.5;text-align:center;overflow-wrap:anywhere;">{{reply_phrase}}</div>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">Application #{{application_id}} · Submitted {{submitted_at}}</p>`,
          "Reply directly to this email to confirm. This message was sent by the {{site_name}} group application system.",
        ),
      },
    },
    completion: {
      zh: {
        subject: "{{site_name}} - {{group_name}} 访问权限已开放",
        html: groupApplicationEmailCard(
          "#15803d",
          "GROUP ACCESS / 分组申请",
          "访问权限已开放",
          `<p style="margin:0 0 18px;font-size:16px;line-height:1.8;">您的邮件确认已验证通过，<strong>{{group_name}}</strong> 分组访问权限现已正式开放。</p>
              <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="width:100%;border-collapse:separate;background:#f0fdf4;border:1px solid #bbf7d0;border-radius:8px;">
                <tr><td style="padding:20px;color:#166534;font-size:15px;line-height:1.8;"><strong>已完成</strong><br>前往 API 密钥页，将需要使用专属能力的密钥切换到 {{group_name}} 分组。</td></tr>
              </table>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">申请编号：#{{application_id}}</p>`,
          "此邮件由 {{site_name}} 的分组申请系统自动发送。",
        ),
      },
      en: {
        subject: "{{site_name}} - {{group_name}} access enabled",
        html: groupApplicationEmailCard(
          "#15803d",
          "GROUP ACCESS",
          "Access is now enabled",
          `<p style="margin:0 0 18px;font-size:16px;line-height:1.8;">Your email confirmation was verified and access to <strong>{{group_name}}</strong> is now enabled.</p>
              <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="width:100%;border-collapse:separate;background:#f0fdf4;border:1px solid #bbf7d0;border-radius:8px;">
                <tr><td style="padding:20px;color:#166534;font-size:15px;line-height:1.8;"><strong>Completed</strong><br>Open the API Keys page and switch the keys that need this access to the {{group_name}} group.</td></tr>
              </table>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">Application #{{application_id}}</p>`,
          "This message was sent automatically by the {{site_name}} group application system.",
        ),
      },
    },
    manual_rejection: {
      zh: {
        subject: "{{site_name}} - {{group_name}} 分组申请未通过",
        html: groupApplicationEmailCard(
          "#b91c1c",
          "GROUP ACCESS / 分组申请",
          "申请未通过",
          `<p style="margin:0 0 18px;font-size:16px;line-height:1.8;">您提交的 <strong>{{group_name}}</strong> 分组申请未获批准。</p>
              <p style="margin:0 0 10px;color:#4b5563;font-size:13px;font-weight:600;">审核理由</p>
              <div style="padding:18px 20px;background:#fef2f2;border:1px solid #fecaca;border-radius:8px;color:#991b1b;font-size:15px;line-height:1.8;overflow-wrap:anywhere;">{{decision_reason}}</div>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">申请编号：#{{application_id}}。如条件发生变化，您可以重新提交申请。</p>`,
          "此邮件由 {{site_name}} 的分组申请系统自动发送。",
        ),
      },
      en: {
        subject: "{{site_name}} - {{group_name}} application declined",
        html: groupApplicationEmailCard(
          "#b91c1c",
          "GROUP ACCESS",
          "Application declined",
          `<p style="margin:0 0 18px;font-size:16px;line-height:1.8;">Your application for <strong>{{group_name}}</strong> was not approved.</p>
              <p style="margin:0 0 10px;color:#4b5563;font-size:13px;font-weight:600;">Decision reason</p>
              <div style="padding:18px 20px;background:#fef2f2;border:1px solid #fecaca;border-radius:8px;color:#991b1b;font-size:15px;line-height:1.8;overflow-wrap:anywhere;">{{decision_reason}}</div>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">Application #{{application_id}}. You may submit a new application if your circumstances change.</p>`,
          "This message was sent automatically by the {{site_name}} group application system.",
        ),
      },
    },
    reply_mismatch: {
      zh: {
        subject: "{{site_name}} - {{group_name}} 邮件确认不匹配",
        html: groupApplicationEmailCard(
          "#b45309",
          "GROUP ACCESS / 分组申请",
          "回复验证未通过",
          `<p style="margin:0 0 18px;font-size:16px;line-height:1.8;">系统未能验证您对 <strong>{{group_name}}</strong> 分组申请的邮件回复。</p>
              <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="width:100%;border-collapse:separate;background:#fffbeb;border:1px solid #fde68a;border-radius:8px;">
                <tr><td style="padding:20px;color:#92400e;font-size:15px;line-height:1.8;">回复正文与要求的严格确认词不完全一致，本次申请已自动拒绝。常见原因包括额外签名、空格或其他文字。</td></tr>
              </table>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">申请编号：#{{application_id}}。您可以重新提交申请并再次完成邮件确认。</p>`,
          "此邮件由 {{site_name}} 的分组申请系统自动发送。",
        ),
      },
      en: {
        subject: "{{site_name}} - {{group_name}} confirmation did not match",
        html: groupApplicationEmailCard(
          "#b45309",
          "GROUP ACCESS",
          "Reply verification failed",
          `<p style="margin:0 0 18px;font-size:16px;line-height:1.8;">We could not verify your email reply for the <strong>{{group_name}}</strong> group application.</p>
              <table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="width:100%;border-collapse:separate;background:#fffbeb;border:1px solid #fde68a;border-radius:8px;">
                <tr><td style="padding:20px;color:#92400e;font-size:15px;line-height:1.8;">The reply body did not exactly match the required phrase, so this application was automatically declined. Common causes include signatures, spaces, or extra text.</td></tr>
              </table>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">Application #{{application_id}}. You may submit a new application and complete confirmation again.</p>`,
          "This message was sent automatically by the {{site_name}} group application system.",
        ),
      },
    },
    revocation: {
      zh: {
        subject: "{{site_name}} - {{group_name}} 访问权限已撤销",
        html: groupApplicationEmailCard(
          "#9f1239",
          "GROUP ACCESS / 分组申请",
          "访问权限已撤销",
          `<p style="margin:0 0 18px;font-size:16px;line-height:1.8;">您的 <strong>{{group_name}}</strong> 分组访问权限已被管理员撤销。</p>
              <p style="margin:0 0 10px;color:#4b5563;font-size:13px;font-weight:600;">撤销理由</p>
              <div style="padding:18px 20px;background:#fff1f2;border:1px solid #fecdd3;border-radius:8px;color:#9f1239;font-size:15px;line-height:1.8;overflow-wrap:anywhere;">{{decision_reason}}</div>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">已绑定该分组的 API 密钥将无法继续使用。申请编号：#{{application_id}}</p>`,
          "此邮件由 {{site_name}} 的分组申请系统自动发送。",
        ),
      },
      en: {
        subject: "{{site_name}} - {{group_name}} access revoked",
        html: groupApplicationEmailCard(
          "#9f1239",
          "GROUP ACCESS",
          "Access revoked",
          `<p style="margin:0 0 18px;font-size:16px;line-height:1.8;">Your access to the <strong>{{group_name}}</strong> group has been revoked by an administrator.</p>
              <p style="margin:0 0 10px;color:#4b5563;font-size:13px;font-weight:600;">Revocation reason</p>
              <div style="padding:18px 20px;background:#fff1f2;border:1px solid #fecdd3;border-radius:8px;color:#9f1239;font-size:15px;line-height:1.8;overflow-wrap:anywhere;">{{decision_reason}}</div>
              <p style="margin:22px 0 0;color:#6b7280;font-size:13px;line-height:1.7;">API keys assigned to this group can no longer be used. Application #{{application_id}}</p>`,
          "This message was sent automatically by the {{site_name}} group application system.",
        ),
      },
    },
  };
}

export const groupApplicationsAdminAPI = {
  async list(
    params: {
      page?: number;
      page_size?: number;
      status?: GroupApplicationStatus | "";
      search?: string;
    } = {},
  ): Promise<GroupApplicationListResult> {
    const { data } = await apiClient.get<GroupApplicationListResult>(
      "/admin/group-applications",
      { params },
    );
    return data;
  },
  async get(id: number, signal?: AbortSignal): Promise<AdminGroupApplication> {
    const { data } = await apiClient.get<AdminGroupApplication>(
      `/admin/group-applications/${id}`,
      { signal },
    );
    return data;
  },
  async listCommunications(
    id: number,
    signal?: AbortSignal,
  ): Promise<GroupApplicationCommunication[]> {
    const { data } = await apiClient.get<GroupApplicationCommunication[]>(
      `/admin/group-applications/${id}/communications`,
      { signal },
    );
    return data;
  },
  async exportCommunications(id: number): Promise<Blob> {
    const { data } = await apiClient.get<Blob>(
      `/admin/group-applications/${id}/communications/export`,
      { responseType: "blob" },
    );
    return data;
  },
  async approve(id: number): Promise<AdminGroupApplication> {
    const { data } = await apiClient.post<AdminGroupApplication>(
      `/admin/group-applications/${id}/approve`,
    );
    return data;
  },
  async reject(id: number, reason: string): Promise<AdminGroupApplication> {
    const { data } = await apiClient.post<AdminGroupApplication>(
      `/admin/group-applications/${id}/reject`,
      { reason },
    );
    return data;
  },
  async revoke(id: number, reason: string): Promise<AdminGroupApplication> {
    const { data } = await apiClient.post<AdminGroupApplication>(
      `/admin/group-applications/${id}/revoke`,
      { reason },
    );
    return data;
  },
  async resendApproval(id: number): Promise<void> {
    await apiClient.post(`/admin/group-applications/${id}/resend-approval`);
  },
  async retryMail(id: number, outboxID: number): Promise<void> {
    await apiClient.post(
      `/admin/group-applications/${id}/mails/${outboxID}/retry`,
    );
  },
  async listPolicies(): Promise<GroupApplicationPolicy[]> {
    const { data } = await apiClient.get<GroupApplicationPolicy[]>(
      "/admin/group-application-policies",
    );
    return data;
  },
  async savePolicy(
    groupID: number,
    policy: GroupApplicationPolicy,
    attachment?: File,
  ): Promise<GroupApplicationPolicy> {
    if (attachment) {
      const form = new FormData();
      form.append("policy", JSON.stringify(policy));
      form.append("attachment", attachment, attachment.name);
      const { data } = await apiClient.put<GroupApplicationPolicy>(
        `/admin/group-application-policies/${groupID}`,
        form,
        { headers: { "Content-Type": "multipart/form-data" } },
      );
      return data;
    }
    const { data } = await apiClient.put<GroupApplicationPolicy>(
      `/admin/group-application-policies/${groupID}`,
      policy,
    );
    return data;
  },
  async downloadAttachment(id: number): Promise<Blob> {
    const { data } = await apiClient.get<Blob>(
      `/admin/group-application-policies/attachments/${id}`,
      { responseType: "blob" },
    );
    return data;
  },
  async getEmailConfig(): Promise<GroupApplicationEmailConfig> {
    const { data } = await apiClient.get<GroupApplicationEmailConfig>(
      "/admin/group-applications/email-config",
    );
    return data;
  },
  async saveEmailConfig(
    input: GroupApplicationEmailConfig,
  ): Promise<GroupApplicationEmailConfig> {
    const { data } = await apiClient.put<GroupApplicationEmailConfig>(
      "/admin/group-applications/email-config",
      input,
    );
    return data;
  },
  async testSMTP(input: GroupApplicationEmailConfig): Promise<void> {
    await apiClient.post(
      "/admin/group-applications/email-config/test-smtp",
      input,
    );
  },
  async sendTestEmail(
    input: GroupApplicationEmailConfig,
    recipient: string,
  ): Promise<void> {
    await apiClient.post("/admin/group-applications/email-config/send-test", {
      config: input,
      recipient,
    });
  },
  async testIMAP(
    input: GroupApplicationEmailConfig,
  ): Promise<{ ok: boolean; mailboxes: string[] }> {
    const { data } = await apiClient.post<{ ok: boolean; mailboxes: string[] }>(
      "/admin/group-applications/email-config/test-imap",
      input,
      { timeout: 15_000 },
    );
    return data;
  },
  async workerStatus(): Promise<GroupApplicationWorkerHealth> {
    const { data } = await apiClient.get<GroupApplicationWorkerHealth>(
      "/admin/group-applications/worker-status",
    );
    return data;
  },
};

export default groupApplicationsAdminAPI;
