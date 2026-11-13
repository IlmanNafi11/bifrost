import { expect, test } from '../../core/fixtures/base.fixture'
import type { VirtualKeysPage } from './pages/virtual-keys.page'

async function skipIfConfigStoreMissing(virtualKeysPage: VirtualKeysPage): Promise<void> {
  const missingConfigStore = await virtualKeysPage.page
    .getByText('Config store setup is missing.')
    .isVisible({ timeout: 1000 })
    .catch(() => false)

  test.skip(missingConfigStore, 'Config store setup is missing; virtual-key E2E tests require a configured config store.')
}

// The block is the one place a key's model surface is edited, and its three states are what the
// server stores: absent (off), empty (deny every model) and populated. Collapsing off and empty
// would silently deny a key the operator meant to leave alone, so each state is asserted on screen.
test.describe('Listed Models', () => {
  test.beforeEach(async ({ virtualKeysPage }) => {
    await virtualKeysPage.goto()
    await skipIfConfigStoreMissing(virtualKeysPage)
  })

  test.afterEach(async ({ virtualKeysPage }) => {
    await virtualKeysPage.closeSheet()
  })

  test('keeps "off" and "on but empty" as two different states', async ({ virtualKeysPage }) => {
    await virtualKeysPage.dismissToasts()
    await virtualKeysPage.createBtn.click()
    await expect(virtualKeysPage.sheet).toBeVisible()
    await virtualKeysPage.waitForSheetAnimation()

    const editorSwitch = virtualKeysPage.page.getByTestId('vk-listed-models-switch')
    const offHint = virtualKeysPage.page.getByTestId('vk-listed-models-off-hint')
    const emptyHint = virtualKeysPage.page.getByTestId('vk-listed-models-empty')

    await editorSwitch.scrollIntoViewIfNeeded()
    await expect(offHint).toBeVisible()
    await expect(emptyHint).toHaveCount(0)

    await editorSwitch.click()
    await expect(emptyHint).toBeVisible()
    await expect(offHint).toHaveCount(0)

    await editorSwitch.click()
    await expect(offHint).toBeVisible()
    await expect(emptyHint).toHaveCount(0)
  })

  test('an empty block cannot be saved without a name, and a named one can', async ({ virtualKeysPage }) => {
    await virtualKeysPage.dismissToasts()
    await virtualKeysPage.createBtn.click()
    await expect(virtualKeysPage.sheet).toBeVisible()
    await virtualKeysPage.waitForSheetAnimation()

    const editorSwitch = virtualKeysPage.page.getByTestId('vk-listed-models-switch')
    await editorSwitch.scrollIntoViewIfNeeded()
    await editorSwitch.click()
    await expect(virtualKeysPage.page.getByTestId('vk-listed-models-empty')).toBeVisible()

    await expect(virtualKeysPage.saveBtn).toBeDisabled()

    await virtualKeysPage.nameInput.fill(`Listed Models Test ${Date.now()}`)
    await expect(virtualKeysPage.saveBtn).toBeEnabled()
  })
})

// The list endpoint leaves `listed_models` out, so the editor's baseline has to come from the
// single-key GET. Without that, reopening a key shows the switch off whatever the server stored,
// and the next Save quietly turns the block off - turning a deny-all key back into a key that can
// call every model its provider configs allow.
test('reopening a saved key shows the stored block, not the off state', async ({ virtualKeysPage }) => {
  await virtualKeysPage.dismissToasts()
  const name = `Listed Models Round Trip ${Date.now()}`

  await virtualKeysPage.createBtn.click()
  await expect(virtualKeysPage.sheet).toBeVisible()
  await virtualKeysPage.waitForSheetAnimation()

  const editorSwitch = virtualKeysPage.page.getByTestId('vk-listed-models-switch')
  const emptyHint = virtualKeysPage.page.getByTestId('vk-listed-models-empty')
  const offHint = virtualKeysPage.page.getByTestId('vk-listed-models-off-hint')

  await virtualKeysPage.nameInput.fill(name)
  await editorSwitch.scrollIntoViewIfNeeded()
  await editorSwitch.click()
  await expect(emptyHint).toBeVisible()
  await expect(virtualKeysPage.saveBtn).toBeEnabled()
  await virtualKeysPage.saveBtn.click()
  await expect(virtualKeysPage.sheet).toBeHidden()

  // Reopen the same key from the table and let the detail query land.
  await virtualKeysPage.openVirtualKeyEditor(name)

  await virtualKeysPage.page.getByTestId('vk-listed-models-switch').scrollIntoViewIfNeeded()
  await expect(emptyHint).toBeVisible()
  await expect(offHint).toHaveCount(0)
})
