import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import { createRouter, createWebHistory } from 'vue-router'
import '@astral/ui/tokens.css'
import 'markstream-vue/index.css'
import './style.css'
import './lib/api'
import { i18n } from './i18n'
import App from './App.vue'

const desktopPlatform = window.astralDesktop?.platform
if (desktopPlatform === 'darwin') document.documentElement.dataset.desktop = 'mac'
else if (desktopPlatform === 'win32') document.documentElement.dataset.desktop = 'win'
else if (desktopPlatform === 'linux') document.documentElement.dataset.desktop = 'linux'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: () => import('./pages/ChatPage.vue') },
    { path: '/sessions/:id', component: () => import('./pages/ChatPage.vue') },
    { path: '/settings/:section?', component: () => import('./pages/SettingsPage.vue') },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})
createApp(App).use(createPinia()).use(PiniaColada).use(i18n).use(router).mount('#app')
