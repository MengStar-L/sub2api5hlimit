import { createApp } from 'vue'
import '@fontsource/noto-sans-sc/chinese-simplified-400.css'
import '@fontsource/noto-sans-sc/chinese-simplified-600.css'
import '@fontsource/noto-sans-sc/chinese-simplified-700.css'
import '@fontsource/sora/latin-500.css'
import '@fontsource/sora/latin-600.css'
import '@fontsource/sora/latin-700.css'
import '@fontsource/jetbrains-mono/latin-500.css'
import '@/styles.css'
import App from '@/App.vue'
import { router } from '@/router'

createApp(App).use(router).mount('#app')
