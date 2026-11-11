/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { HostedAPIs } from '..'
import type { HostedState } from '../api'

afterEach(() => vi.restoreAllMocks())

const free: HostedState = {
  upstreams: [],
  platform_fallback: false,
  connection_limit: 1,
  daily_token_limit: 10000000,
  used_tokens: 1234,
  day: '2026-11-11',
  tier: 'default',
  base_path: '/v1/hosted',
}

async function renderPage(data: HostedState) {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/hosted') return { data: { success: true, data } }
    if (url === '/api/verify/methods') {
      return {
        data: {
          success: true,
          data: {
            scope: 'hosted_api.manage',
            methods: [{ method: 'password', available: true }],
            oauth_providers: [],
            password_encryption_enabled: false,
          },
        },
      }
    }
    throw new Error(`Unexpected GET ${url}`)
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const root = createRootRoute({ component: HostedAPIs })
  const route = createRoute({ getParentRoute: () => root, path: '/' })
  const router = createRouter({
    routeTree: root.addChildren([route]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await screen.findByText('Private API aggregation')
}

it('keeps paid fallback off and opens a price confirmation before verification', async () => {
  await renderPage(free)
  const toggle = screen.getByRole('switch', { name: 'Paid platform fallback' })
  expect(toggle).not.toBeChecked()
  await userEvent.click(toggle)
  expect(
    await screen.findByRole('alertdialog', {
      name: 'Enable paid platform fallback?',
    })
  ).toBeVisible()
  expect(
    screen.getByText(
      'Platform fallback spends your platform balance using your API key group pricing. It only starts before a private response begins.'
    )
  ).toBeVisible()
  expect(toggle).not.toBeChecked()
  await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  expect(toggle).not.toBeChecked()
})

it('opens a usable add form and hides its secret input', async () => {
  await renderPage(free)
  await userEvent.click(
    screen.getByRole('button', { name: 'Connect an upstream' })
  )
  expect(
    await screen.findByRole('dialog', { name: 'Connect an upstream' })
  ).toBeVisible()
  const secret = screen.getByLabelText('Upstream API key')
  expect(secret).toHaveAttribute('type', 'password')
  await userEvent.type(screen.getByLabelText('Name'), 'My provider')
  await userEvent.type(
    screen.getByLabelText('Upstream Base URL'),
    'https://upstream.example/v1'
  )
  await userEvent.type(
    screen.getByLabelText('Model IDs, one per line'),
    'chat-a\nchat-b'
  )
  await userEvent.click(screen.getByRole('button', { name: 'Save' }))
  expect(await screen.findByText('Enter the upstream API key')).toBeVisible()
  expect(secret).toHaveAttribute('aria-invalid', 'true')
})

it('blocks new free connections at the upstream limit while retaining edit and delete', async () => {
  await renderPage({
    ...free,
    upstreams: [
      {
        id: 1,
        name: 'My provider',
        base_url: 'https://upstream.example/v1',
        key_hint: 'abcd',
        models: '["chat-a","chat-b"]',
        enabled: false,
      },
    ],
  })
  expect(
    screen.getByRole('button', { name: 'Connect an upstream' })
  ).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Edit' })).toBeEnabled()
  await userEvent.click(screen.getByRole('button', { name: 'Delete' }))
  expect(
    await screen.findByRole('alertdialog', { name: 'Delete upstream?' })
  ).toBeVisible()
  expect(
    screen.getByText(
      'The stored upstream key will be deleted. Existing daily usage will remain.'
    )
  ).toBeVisible()
})

it('keeps a fifth VIP slot available and shows the account-wide daily allowance', async () => {
  await renderPage({
    ...free,
    tier: 'vip',
    connection_limit: 5,
    daily_token_limit: 100000000,
    upstreams: Array.from({ length: 4 }, (_, index) => ({
      id: index + 1,
      name: `Provider ${index}`,
      base_url: 'https://upstream.example/v1',
      key_hint: 'abcd',
      models: '["chat-a"]',
      enabled: true,
    })),
  })
  expect(
    screen.getByRole('button', { name: 'Connect an upstream' })
  ).toBeEnabled()
  expect(screen.getByText('1,234 / 100,000,000')).toBeVisible()
})
