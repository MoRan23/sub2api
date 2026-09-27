package service

import (
	"errors"
	"strings"
	"testing"
)

const candyCorrectTable = `| 问题 | 最少数量 | 最优取法（简洁） |
| --- | ---: | --- |
| 第1问：固定取法 | 32 | 圆感与非圆感各自预先分配。 |
| 第2问：自适应取法 | 29 | 根据已取出的糖调整。 |
| 第3问：固定取法 | 40 | 取葡萄，策略中可以出现 38 和 99。 |
| 第3问：自适应取法 | 38 | 根据结果调整。 |`

func TestGradeCandyAnswerAccepted(t *testing.T) {
	tests := []struct {
		name string
		text string
		want [4]int
	}{
		{"correct", candyCorrectTable, [4]int{32, 29, 40, 38}},
		{"one_wrong_is_not_parse_failure", strings.Replace(candyCorrectTable, "| 29 |", "| 30 |", 1), [4]int{32, 30, 40, 38}},
		{"all_wrong_are_not_parse_failure", "| 问题 | 数量 |\n| 第一问 | 1 |\n| 第二问 | 2 |\n| 第三问固定 | 3 |\n| 第三问自适应 | 4 |", [4]int{1, 2, 3, 4}},
		{"bold_spacing_suffix", strings.NewReplacer("第1问", "**第 1 问**", "| 32 |", "| **32 颗** |", "| 29 |", "| 最少 29 颗糖果 |", "| 40 |", "| 总共40颗 |", "| 38 |", "| `38` |", "\n", "\r\n").Replace(candyCorrectTable), [4]int{32, 29, 40, 38}},
		{"fullwidth", strings.NewReplacer("|", "｜", "1", "１", "2", "２", "3", "３", "4", "４", "8", "８", "9", "９", ":", "：").Replace(candyCorrectTable), [4]int{32, 29, 40, 38}},
		{"chinese_numbers", strings.NewReplacer("| 32 |", "| 三十二颗 |", "| 29 |", "| 二十九 |", "| 40 |", "| 四十 |", "| 38 |", "| 叁拾捌 |", "第1问", "第一问", "第2问", "第二问", "第3问", "第三问").Replace(candyCorrectTable), [4]int{32, 29, 40, 38}},
		{"digit_style_chinese", strings.NewReplacer("| 32 |", "| 三二 |", "| 29 |", "| 二九 |", "| 40 |", "| 四〇 |", "| 38 |", "| 三八 |").Replace(candyCorrectTable), [4]int{32, 29, 40, 38}},
		{"numbered_labels", strings.NewReplacer("第1问", "1.", "第2问", "2.", "第3问", "3.").Replace(candyCorrectTable), [4]int{32, 29, 40, 38}},
		{"english_labels", strings.NewReplacer("问题", "Question", "最少数量", "Minimum count", "第1问：固定取法", "Q1 fixed", "第2问：自适应取法", "Q2 adaptive", "第3问：固定取法", "Q3 fixed", "第3问：自适应取法", "Question 3 adaptive").Replace(candyCorrectTable), [4]int{32, 29, 40, 38}},
		{"q3_reference_rules", strings.NewReplacer("第3问：固定取法", "第3问（按第1问规则）", "第3问：自适应取法", "第3问（按第2问规则）").Replace(candyCorrectTable), [4]int{32, 29, 40, 38}},
		{"nonadaptive_is_fixed", strings.ReplaceAll(candyCorrectTable, "固定取法", "非自适应"), [4]int{32, 29, 40, 38}},
		{"restricted_extra_row", candyCorrectTable + "\n| 第2问：受限的先圆后非圆策略 | 32 | 仅这一限制下需要32 |", [4]int{32, 29, 40, 38}},
		{"two_phase_extra_row", candyCorrectTable + "\n| 第2问：先把圆感糖摸到满意，再只摸非圆感糖 | 32 | |", [4]int{32, 29, 40, 38}},
		{"unrestricted_not_excluded", strings.Replace(candyCorrectTable, "第2问：自适应取法", "第2问：不受限制的自适应取法", 1), [4]int{32, 29, 40, 38}},
		{"quoted_wrong_table_ignored", "> " + strings.ReplaceAll(strings.Replace(candyCorrectTable, "| 29 |", "| 32 |", 1), "\n", "\n> ") + "\n\n" + candyCorrectTable, [4]int{32, 29, 40, 38}},
		{"strategies_and_prose_ignored", "此前有人写出32、32、40、38，这是其错误答案。\n\n" + candyCorrectTable + "\n\n策略中提到另取7颗、再取32颗；并非修改表格。", [4]int{32, 29, 40, 38}},
		{"identical_duplicate_answers", candyCorrectTable + "\n\n" + candyCorrectTable, [4]int{32, 29, 40, 38}},
		{"fenced_table", "```markdown\n" + candyCorrectTable + "\n```", [4]int{32, 29, 40, 38}},
		{"tab_table", strings.ReplaceAll(strings.Trim(candyCorrectTable, "|"), "|", "\t"), [4]int{32, 29, 40, 38}},
		{"escaped_pipe_in_strategy", strings.Replace(candyCorrectTable, "根据已取出的糖调整。", `方案 A \| 方案 B，根据已取出的糖调整。`, 1), [4]int{32, 29, 40, 38}},
		{"reordered_columns", "| 取法 | 最少颗数 | 问题 |\n| 提到99 | 32 | 第1问 |\n| 提到32 | 29 | 第2问 |\n| 40+38不是颗数列 | 40 | 第3问固定 |\n| 提到40 | 38 | 第3问自适应 |", [4]int{32, 29, 40, 38}},
		{"paired_q3", "| 问题 | 最少数量 | 取法 |\n| 第1问 | 32 | |\n| 第2问 | 29 | |\n| 第3问 | 固定：40颗；自适应：38颗 | 两套取法 |", [4]int{32, 29, 40, 38}},
		{"paired_q3_reverse", "| 问题 | 最少数量 |\n| 第1问 | 32 |\n| 第2问 | 29 |\n| 第3问（固定/自适应） | 自适应：38 / 固定：40 |", [4]int{32, 29, 40, 38}},
		{"paired_q3_br", "| 问题 | 最少数量 |\n| 第1问 | 32 |\n| 第2问 | 29 |\n| 第3问 | 第1问规则：40<br/>第2问规则：38 |", [4]int{32, 29, 40, 38}},
		{"zero_and_negative_are_wrong_counts", strings.NewReplacer("| 32 |", "| 0 |", "| 29 |", "| -1 |").Replace(candyCorrectTable), [4]int{0, -1, 40, 38}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GradeCandyAnswer(tc.text)
			if err != nil || got != tc.want {
				t.Fatalf("GradeCandyAnswer() = %v, %v, want %v", got, err, tc.want)
			}
		})
	}
}

func TestGradeCandyAnswerRejectsUncertainAnswers(t *testing.T) {
	tests := []struct {
		name string
		text string
		code string
	}{
		{"empty", "", candyGradeMissing},
		{"numbers_without_table", "答案：32、29、40、38", candyGradeMissing},
		{"unlabelled_table", "| 32 | 29 | 40 | 38 |", candyGradeMissing},
		{"quoted_only", "> " + strings.ReplaceAll(candyCorrectTable, "\n", "\n> "), candyGradeMissing},
		{"missing_q2", strings.Replace(candyCorrectTable, "| 第2问：自适应取法 | 29 | 根据已取出的糖调整。 |\n", "", 1), candyGradeMissing},
		{"restricted_only_q2", strings.Replace(candyCorrectTable, "第2问：自适应取法", "第2问：受限两阶段", 1), candyGradeMissing},
		{"duplicate_conflict", candyCorrectTable + "\n| 第2问 | 32 | |", candyGradeAmbiguous},
		{"cross_table_conflict", candyCorrectTable + "\n\n" + strings.Replace(candyCorrectTable, "| 38 |", "| 39 |", 1), candyGradeAmbiguous},
		{"conflicting_q1_mode", strings.Replace(candyCorrectTable, "第1问：固定取法", "第1问：自适应取法", 1), candyGradeAmbiguous},
		{"contradictory_q2_modes", strings.Replace(candyCorrectTable, "第2问：自适应取法", "第2问：固定、自适应", 1), candyGradeAmbiguous},
		{"bare_q3", strings.Replace(candyCorrectTable, "第3问：固定取法", "第3问", 1), candyGradeAmbiguous},
		{"q3_pair_without_labels", "| 问题 | 最少数量 |\n| 第1问 | 32 |\n| 第2问 | 29 |\n| 第3问 | 40 / 38 |", candyGradeAmbiguous},
		{"q3_pair_duplicate_mode", "| 问题 | 最少数量 |\n| 第1问 | 32 |\n| 第2问 | 29 |\n| 第3问 | 固定40；固定38 |", candyGradeAmbiguous},
		{"number_in_strategy_not_answer", strings.Replace(candyCorrectTable, "| 29 |", "| 不确定 |", 1), candyGradeInvalid},
		{"multiple_counts_in_cell", strings.Replace(candyCorrectTable, "| 29 |", "| 29或32 |", 1), candyGradeInvalid},
		{"formula_not_count", strings.Replace(candyCorrectTable, "| 32 |", "| 16+16=32 |", 1), candyGradeInvalid},
		{"decimal", strings.Replace(candyCorrectTable, "| 29 |", "| 29.5 |", 1), candyGradeInvalid},
		{"overflow", strings.Replace(candyCorrectTable, "| 29 |", "| 99999999999999999999999999 |", 1), candyGradeInvalid},
		{"chinese_ambiguous_sequence", strings.Replace(candyCorrectTable, "| 32 |", "| 三十二十九 |", 1), candyGradeInvalid},
		{"colloquial_chinese_ambiguous", strings.Replace(candyCorrectTable, "| 32 |", "| 二百三 |", 1), candyGradeInvalid},
		{"malformed_incomplete_cell", strings.Replace(candyCorrectTable, "| 38 |", "| 3… |", 1), candyGradeInvalid},
		{"quantity_column_not_inherited_from_unrelated_table", candyCorrectTable + "\n\n附录\n| 第2问 | 32 | 这没有答案表头 |", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := GradeCandyAnswer(tc.text)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var gradeErr *CandyAnswerGradeError
			if !errors.As(err, &gradeErr) || gradeErr.Code != tc.code {
				t.Fatalf("GradeCandyAnswer() error = %v, want code %s", err, tc.code)
			}
			if strings.Contains(err.Error(), tc.text) && tc.text != "" {
				t.Fatal("grading error must not include raw response")
			}
		})
	}
}

func TestCandyPromptVersionAndKeyIsolation(t *testing.T) {
	if CandyTestPromptVersion == "" {
		t.Fatal("prompt requires a version")
	}
	for _, part := range []string{"不允许联网、运行代码或调用任何外部工具", "全部葡萄味心形", "西瓜味糖果一共 15 颗", "五角星和心形摸起来完全一样", "仍然是五角星", "三颗糖中必须有一颗葡萄味", "用四行分别回答"} {
		if !strings.Contains(CandyTestPrompt, part) {
			t.Errorf("prompt missing question condition %q", part)
		}
	}
	for _, secret := range []string{"29", "40", "38"} {
		if strings.Contains(CandyTestPrompt, secret) {
			t.Errorf("prompt must not reveal grading answer %s", secret)
		}
	}
	first := ExpectedCandyAnswerCounts()
	first[0] = 999
	if got := ExpectedCandyAnswerCounts(); got != [4]int{32, 29, 40, 38} {
		t.Fatalf("caller modified grading key: %v", got)
	}
}

func FuzzGradeCandyAnswer(f *testing.F) {
	f.Add(candyCorrectTable)
	f.Add("|问题|最少数量|\n|第3问|固定40;自适应38|")
	f.Add("第2问29颗")
	f.Fuzz(func(t *testing.T, text string) {
		_, err := GradeCandyAnswer(text)
		if err != nil {
			var gradeErr *CandyAnswerGradeError
			if !errors.As(err, &gradeErr) {
				t.Fatalf("untyped failure: %v", err)
			}
		}
	})
}
