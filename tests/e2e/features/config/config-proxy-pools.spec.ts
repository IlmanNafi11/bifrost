import { expect, test } from '../../core/fixtures/base.fixture'

test.describe('Proxy Pools', () => {
  test('should render the proxy pools page and its action bar', async ({ configSettingsPage }) => {
    await configSettingsPage.goto('proxy-pools')
    await expect(configSettingsPage.page.getByTestId('proxy-pool-nav')).toBeVisible()
    await expect(configSettingsPage.page.getByTestId('proxy-pool-table')).toBeVisible()
    await expect(configSettingsPage.page.getByTestId('proxy-pool-add-btn')).toBeVisible()
  })
})
