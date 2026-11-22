import {
  sqliteTable,
  text,
  integer,
  primaryKey,
  index,
} from "drizzle-orm/sqlite-core";
export const devices = sqliteTable("devices", {
  id: text("id").primaryKey(),
  connectionId: text("connection_id").notNull().unique(),
  name: text("name").notNull(),
  public: text("public_key").notNull(),
  tokenHash: text("token_hash").notNull().unique(),
  host: integer("host", { mode: "boolean" }).notNull(),
  seen: integer("seen").notNull(),
});
export const grants = sqliteTable(
  "device_grants",
  {
    host: text("host").notNull(),
    client: text("client").notNull(),
    pairing: text("pairing").notNull().default(""),
  },
  (t) => [primaryKey({ columns: [t.host, t.client] })],
);
export const pairings = sqliteTable(
  "pairings",
  {
    id: text("id").primaryKey(),
    host: text("host").notNull(),
    client: text("client").notNull(),
    name: text("name").notNull(),
    public: text("public_key").notNull(),
    envelope: text("envelope").notNull(),
    reply: text("reply"),
    state: text("state").notNull(),
    expires: integer("expires").notNull(),
  },
  (t) => [
    index("pairings_host").on(t.host, t.state),
    index("pairings_expiry").on(t.expires),
  ],
);
export const metadata = sqliteTable("metadata", {
  key: text("key").primaryKey(),
  value: text("value").notNull(),
});
export type DeviceRow = typeof devices.$inferSelect;
