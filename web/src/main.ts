import { createApp } from 'vue'
// 中文正文与标题（Fraunces 无中文字形，靠逐字回退到这里）
import '@fontsource/noto-sans-sc/chinese-simplified-400.css'
import '@fontsource/noto-sans-sc/chinese-simplified-600.css'
import '@fontsource/noto-sans-sc/chinese-simplified-700.css'
// 等宽：Key 掩码、金额、时间
import '@fontsource/jetbrains-mono/latin-500.css'
// 拉丁正文（Inter）与装饰性衬线（Fraunces）：见 styles/fonts.css，只取 latin 子集
import '@/styles.css'
import App from '@/App.vue'
import { router } from '@/router'

createApp(App).use(router).mount('#app')
