// 模态面板的小工具。

interface Inertable {
  inert: boolean
}

/**
 * 面板打开时把它的兄弟元素（页面、顶栏、底部导航）设为 inert：Tab、Enter 到不了背后的批准、拒绝按钮，读屏也读不到。
 * keep 里的（面板本身、点了能关面板的背景）不动。返回恢复函数，恢复成各自原来的值。
 */
export function inertSiblings<T extends Inertable>(siblings: Iterable<T>, keep: readonly T[]): () => void {
  const changed: [T, boolean][] = []
  for (const el of siblings) {
    if (keep.includes(el)) continue
    changed.push([el, el.inert])
    el.inert = true
  }
  return () => {
    for (const [el, was] of changed) el.inert = was
  }
}

/**
 * 页面底部要给固定的结果提示留出的高度：从提示的上沿到底部导航的上沿（页面内容在导航之上结束），向上取整，不为负。
 * 窄屏、文案长时提示会变高，固定的留白不够，最后一行会压在提示下面。
 */
export function toastSpace(navTop: number, toastTop: number): number {
  return Math.max(0, Math.ceil(navTop - toastTop))
}
