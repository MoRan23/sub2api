import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/vue";
import GroupApplicationTemplateEditor from "./GroupApplicationTemplateEditor.vue";
import { defaultGroupApplicationTemplates } from "@/api/admin/groupApplications";

vi.mock("vue-i18n", async (importOriginal) => {
  const actual = await importOriginal<typeof import("vue-i18n")>();
  const translations: Record<string, string> = {
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
  };
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => translations[key] ?? key }),
  };
});

describe("GroupApplicationTemplateEditor", () => {
  it("edits only the selected template kind and locale", async () => {
    const templates = defaultGroupApplicationTemplates();
    render(GroupApplicationTemplateEditor, {
      props: { modelValue: templates },
    });

    const originalCompletion = templates.completion.zh.subject;
    await fireEvent.update(screen.getByLabelText("Subject"), "批准主题");
    expect(templates.approval.zh.subject).toBe("批准主题");
    expect(templates.completion.zh.subject).toBe(originalCompletion);

    await fireEvent.click(screen.getByRole("button", { name: "Template type" }));
    await fireEvent.click(await screen.findByRole("option", { name: "Completion" }));
    expect((screen.getByLabelText("Subject") as HTMLInputElement).value).toBe(
      originalCompletion,
    );

    await fireEvent.click(screen.getByRole("tab", { name: "English" }));
    await fireEvent.update(screen.getByLabelText("Subject"), "Completed EN");
    expect(templates.completion.en.subject).toBe("Completed EN");
    expect(templates.completion.zh.subject).toBe(originalCompletion);
  });
});
