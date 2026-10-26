package service

// Legacy Go/API names remain stable; new jobs exclusively use pelican-v1.
const CandyTestPromptVersion = "pelican-v1"

const CandyTestPrompt = `创建一个HTML，内容是SVG绘制一个鹈鹕骑自行车的2D动画，你不需要进行任何测试`

const pelicanTestInstructions = "直接返回完整、独立的 HTML，不使用 Markdown 代码块或外部依赖。只输出 HTML，不要解释。"
