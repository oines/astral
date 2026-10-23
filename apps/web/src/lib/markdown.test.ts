import { describe, expect, it } from 'vitest'
import { getMarkdown, parseMarkdownToStructure } from 'markstream-vue'
import { configureMarkdown, protectCurrency } from './markdown'

function mathNodes(source: string, final = true) {
  const md = configureMarkdown(getMarkdown(`currency-${crypto.randomUUID()}`))
  const nodes = parseMarkdownToStructure(source, md, { final })
  return JSON.stringify(nodes).match(/"type":"math_(?:inline|block)"/g) || []
}

describe('currency and mathematical notation', () => {
  it('keeps prices and their surrounding prose out of math, including incomplete streams', () => {
    const source = 'GOAT（$10/月，整池 $70 用量），额度 **$60/月**。每 5 小时 $14、每周 $35。\n\n计价 $0.16 输入 / $0.58 输出 / $0.016 缓存读。\n\n只有 $1 的 Go 套餐里，Fast 的额度只有 $6；$20 的 Pro 是 $70。\n\n这 $60 是整个 $70 共享池里的上限。'
    expect(mathNodes(source)).toHaveLength(0)
    expect(mathNodes('$10 per month or $20 per year')).toHaveLength(0)
    expect(mathNodes('$10–$20 and $30-$40')).toHaveLength(0)
    expect(mathNodes('当前额度 $60/月', false)).toHaveLength(0)
    expect(protectCurrency('$10 $20')).toBe('\\$10 \\$20')
  })
  it('preserves numeric math, explicit TeX, code and link destinations', () => {
    const source = '$2x + 1$ and $2 + 3 = 5$ and $10$ and $E=mc^2$\n\n$$\\frac{1}{2}=0.5$$'
    expect(protectCurrency(source)).toBe(source)
    expect(mathNodes(source)).toHaveLength(5)
    const literal = '`$10 and $20` ``a `$30` b`` [amount](https://example.test/$10) https://example.test/$20 \\$30 \\(\\text{cost $40}\\)'
    expect(protectCurrency(literal)).toBe(literal)
    expect(mathNodes('```sh\necho $10 $20\n```')).toHaveLength(0)
  })
})
