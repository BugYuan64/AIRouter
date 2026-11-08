import { Icon } from '@iconify/vue'
import { defineComponent, h } from 'vue'
import type { PropType } from 'vue'

/**
 * 统一图标封装。图标数据来自 @iconify-json/lucide（本地打包，不请求 CDN）。
 * 默认 1em 尺寸并继承 currentColor，便于在任意排版中内联。
 */
export const AppIcon = defineComponent({
  name: 'AppIcon',
  props: {
    name: { type: String as PropType<string>, required: true },
    size: { type: [Number, String] as PropType<number | string>, default: '1em' },
    strokeWidth: { type: [Number, String] as PropType<number | string>, default: 1.75 },
  },
  setup(props) {
    return () =>
      h(Icon, {
        icon: props.name,
        width: props.size,
        height: props.size,
        style: { display: 'inline-block', verticalAlign: '-0.125em', flexShrink: 0 },
        'stroke-width': props.strokeWidth,
      })
  },
})

export default AppIcon
