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

    // Save is gated on the schema resolver, which only reports the missing name once the form has
    // been submitted. A submit that reaches the handler is refused locally and names the field.
    await virtualKeysPage.saveBtn.click()
    await expect(virtualKeysPage.page.getByText('Virtual key name is required')).toBeVisible()
    await expect(virtualKeysPage.sheet).toBeVisible()

    await virtualKeysPage.nameInput.fill(`Listed Models Test ${Date.now()}`)
    await expect(virtualKeysPage.saveBtn).toBeEnabled()
  })
})

// Adding and removing an entry is what the stored block actually holds, so the row has to survive a
// save and a reopen: an editor that only drew a row would leave a key looking narrowed while it can
// still call every model its provider configs allow.
test('adds an entry, saves it, and removes it after a reopen', async ({ virtualKeysPage, request }) => {
  await virtualKeysPage.dismissToasts()
  const name = `Listed Models Add Remove ${Date.now()}`

  // The picker reads its models from the catalog, which this environment has no key to populate.
  // What is under test is the editor's own add/remove and the round trip, so the catalog is stubbed.
  await virtualKeysPage.page.route(
    (url) => url.pathname.endsWith('/api/models'),
    (route) => route.fulfill({ json: { models: [{ name: 'gpt-4o-mini' }], total: 1 } }),
  )

  const created = await request.post('/api/governance/virtual-keys', {
    data: {
      name,
      is_active: true,
      provider_configs: [{ provider: 'openai', allowed_models: ['*'], blacklisted_models: [], allow_all_keys: true }],
    },
  })
  expect(created.ok(), await created.text()).toBeTruthy()
  const vkId: string = (await created.json()).virtual_key.id

  try {
    await virtualKeysPage.goto()
    await skipIfConfigStoreMissing(virtualKeysPage)
    await virtualKeysPage.openVirtualKeyEditor(name)

    const page = virtualKeysPage.page
    const editorSwitch = page.getByTestId('vk-listed-models-switch')
    await editorSwitch.scrollIntoViewIfNeeded()
    await editorSwitch.click()
    await expect(page.getByTestId('vk-listed-models-empty')).toBeVisible()

    // Stage the model through the popover a user picks from, then add it as a row.
    await page.getByTestId('vk-listed-models-model-select').getByRole('combobox').click()
    const popover = page.locator('[data-slot="popover-content"]').last()
    const search = popover.locator('input').first()
    await search.waitFor({ state: 'visible', timeout: 5000 })
    await search.fill('gpt-4o-mini')
    await popover.getByRole('option', { name: /gpt-4o-mini/i }).first().click({ force: true })
    await page.keyboard.press('Escape')
    await page.getByTestId('vk-listed-models-add-model').click()

    const row = page.getByTestId('vk-listed-models-row-0')
    await expect(row).toContainText('openai/gpt-4o-mini')
    await expect(page.getByTestId('vk-listed-models-empty')).toHaveCount(0)

    await expect(virtualKeysPage.saveBtn).toBeEnabled()
    await virtualKeysPage.saveBtn.click()
    await expect(virtualKeysPage.sheet).toBeHidden()

    // Reopening reads the block from the single-key GET, so the row coming back is the stored one.
    await virtualKeysPage.openVirtualKeyEditor(name)
    await page.getByTestId('vk-listed-models-row-0').scrollIntoViewIfNeeded()
    await expect(page.getByTestId('vk-listed-models-row-0')).toContainText('openai/gpt-4o-mini')

    await page.getByTestId('vk-listed-models-remove-0').click()
    await expect(page.getByTestId('vk-listed-models-row-0')).toHaveCount(0)
    await expect(page.getByTestId('vk-listed-models-empty')).toBeVisible()
  } finally {
    await request.delete(`/api/governance/virtual-keys/${vkId}`).catch(() => undefined)
  }
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
