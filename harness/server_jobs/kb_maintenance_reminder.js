export const kbMaintenanceReminderConfigSchema = Object.freeze({
  type: "object",
  additionalProperties: false,
  required: ["from", "to", "subject", "body"],
  properties: {
    from: { type: "string", minLength: 1, maxLength: 128 },
    to: { type: "string", minLength: 1, maxLength: 128 },
    subject: { type: "string", minLength: 1, maxLength: 256 },
    body: { type: "string", minLength: 1, maxLength: 4_096 },
  },
});

export function kbMaintenanceReminderPolicy({ config }) {
  return {
    subject: config.subject,
    body: config.body,
  };
}
