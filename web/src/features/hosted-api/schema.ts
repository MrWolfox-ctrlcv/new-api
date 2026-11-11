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
import z from 'zod'

export const upstreamSchema = z.object({
  name: z
    .string()
    .trim()
    .min(1, 'Invalid hosted API request')
    .max(80, 'Invalid hosted API request'),
  base_url: z
    .url('Use an HTTPS Base URL')
    .refine((value) => value.startsWith('https://'), 'Use an HTTPS Base URL'),
  key: z
    .string()
    .refine(
      (value) => value === '' || (value.length >= 8 && value.length <= 4096),
      'Invalid upstream API key'
    ),
  models: z
    .string()
    .trim()
    .min(1, 'Invalid hosted model list')
    .max(20000, 'Invalid hosted model list')
    .refine((value) => {
      const models = value
        .split(/[\n,]/)
        .map((name) => name.trim())
        .filter(Boolean)
      return (
        models.length > 0 &&
        models.length <= 100 &&
        models.every((name) => name.length <= 200)
      )
    }, 'Invalid hosted model list'),
  enabled: z.boolean(),
})
export type UpstreamFormValues = z.infer<typeof upstreamSchema>
