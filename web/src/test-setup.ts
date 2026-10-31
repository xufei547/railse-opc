// vitest 全局：现有测试都断言中文，把语言钉死为 zh；英文用例在用例里 setLang('en') 并在 afterEach 恢复。
import { setLang } from './i18n'

setLang('zh')
