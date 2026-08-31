import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { describe, expect, it } from 'vitest'
import SideDrawer from '@/components/SideDrawer.vue'

describe('SideDrawer keyboard behavior', () => {
  it('traps focus, closes on Escape, and restores focus', async () => {
    const trigger = document.createElement('button')
    trigger.textContent = '打开'
    document.body.appendChild(trigger)
    trigger.focus()

    const wrapper = mount(SideDrawer, {
      attachTo: document.body,
      props: { open: true, title: '编辑用户' },
      slots: {
        default: '<input aria-label="用户名"><button type="button">保存</button>',
      },
    })
    await nextTick()
    await nextTick()

    const panel = document.querySelector<HTMLElement>('.drawer-panel')
    const buttons = Array.from(panel?.querySelectorAll<HTMLButtonElement>('button') ?? [])
    expect(buttons.length).toBeGreaterThanOrEqual(2)
    expect(document.activeElement).toBe(buttons[0])

    buttons.at(-1)?.focus()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(buttons[0])

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    expect(wrapper.emitted('close')).toHaveLength(1)
    await wrapper.setProps({ open: false })
    await nextTick()
    expect(document.activeElement).toBe(trigger)

    wrapper.unmount()
    trigger.remove()
  })
})
