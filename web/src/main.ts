import { createApp } from 'vue'
import { ElAlert, ElDialog, ElInput, ElOption, ElSelect, ElTable, ElTableColumn, vLoading } from 'element-plus'
import './style.css'
import App from './App.vue'
import router from './router'

// 按需注册：全量 app.use(ElementPlus) 会把所有组件与图标打进产物。
// 新增组件时在此显式添加，避免体积悄悄膨胀。
const app = createApp(App)
app.component('ElAlert', ElAlert)
app.component('ElDialog', ElDialog)
app.component('ElInput', ElInput)
app.component('ElSelect', ElSelect)
app.component('ElOption', ElOption)
app.component('ElTable', ElTable)
app.component('ElTableColumn', ElTableColumn)
app.directive('loading', vLoading)
app.use(router)
app.mount('#app')
