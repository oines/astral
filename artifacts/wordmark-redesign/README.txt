Astral 字标重绘 · 方案 01

astral-lockup.svg — 图形 + 深紫字标，用于浅色背景。
astral-lockup-inverse.svg — 图形 + 淡白字标，用于深色背景。
astral-wordmark.svg — 独立深紫字标。
astral-wordmark-inverse.svg — 独立淡白字标。

设计：沿用 Astral 现有星球与星环图形。六个字母分别绘制为 SVG 轮廓，
收轻笔画、柔化端点，采用单层 a；A 的微弧横笔轻微上扬，呼应星环。
t 和 l 的弯曲收尾保持共同的曲线语言。字距按实际视觉间隙调整。

技术：纯 SVG，无 text、外部字体、位图或远程依赖。文字部分为填充路径。
背景透明。图文组合保持 130 × 40 viewBox，与现版相同。
颜色由 SVG 根节点 color 控制；内联时可用 CSS color 覆盖。

此方案已用于应用的带文字 Logo。应用资源位于 packages/icons/assets/，
由共享 AstralLogo 组件按明暗主题切换，并校正原始画布留白以保持视觉对齐。
preview.png / preview.html 提供新旧对比、深色背景和小尺寸预览。
