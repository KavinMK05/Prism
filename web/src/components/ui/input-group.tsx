"use client"

import * as React from "react"

import { cn } from "@/lib/utils"
import { Input } from "@/components/ui/input"

function InputGroup({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="input-group"
      role="group"
      className={cn(
        "relative flex w-full min-w-0 items-center rounded-md border border-input shadow-xs transition-colors focus-within:ring-[3px] focus-within:ring-ring/50",
        className
      )}
      {...props}
    />
  )
}

function InputGroupInput({
  className,
  ...props
}: React.ComponentProps<"input">) {
  return (
    <Input
      data-slot="input-group-control"
      className={cn("min-w-0 flex-1 border-0 bg-transparent shadow-none focus-visible:ring-0", className)}
      {...props}
    />
  )
}

function InputGroupAddon({
  className,
  align = "inline-start",
  ...props
}: React.ComponentProps<"div"> & { align?: "inline-start" | "inline-end" }) {
  return (
    <div
      role="group"
      data-slot="input-group-addon"
      className={cn(
        "flex shrink-0 items-center justify-center px-3 text-muted-foreground",
        align === "inline-start" ? "order-first" : "order-last",
        className
      )}
      onClick={(e) => {
        if ((e.target as HTMLElement).closest("button")) return
        e.currentTarget.parentElement?.querySelector("input")?.focus()
      }}
      {...props}
    />
  )
}

function InputGroupText({ className, ...props }: React.ComponentProps<"span">) {
  return (
    <span
      className={cn("flex items-center [&_svg]:pointer-events-none", className)}
      {...props}
    />
  )
}

export { InputGroup, InputGroupAddon, InputGroupText, InputGroupInput }
