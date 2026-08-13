"use client"

import { format } from "date-fns"
import { CalendarIcon, ClockIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { buttonVariants } from "@/components/ui/button"
import { Calendar } from "@/components/ui/calendar"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  InputGroup,
  InputGroupInput,
  InputGroupAddon,
} from "@/components/ui/input-group"

function toTimeString(d: Date): string {
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`
}

export function DateTimePicker({
  value,
  onValueChange,
  displayFormat = "MMM d, yyyy h:mm a",
  triggerClassName,
}: {
  value: Date | undefined
  onValueChange: (d: Date | undefined) => void
  displayFormat?: string
  triggerClassName?: string
}) {
  const selected = value ?? new Date()

  return (
    <Popover>
      <PopoverTrigger
        className={cn(
          buttonVariants({ variant: "outline" }),
          "justify-between font-normal",
          triggerClassName
        )}
      >
        <span>
          {value ? (
            format(value, displayFormat)
          ) : (
            <span className="text-muted-foreground">Pick a date</span>
          )}
        </span>
        <CalendarIcon className="size-4 opacity-50" />
      </PopoverTrigger>
      <PopoverContent className="w-auto p-0" align="start">
        <Calendar
          mode="single"
          selected={selected}
          onSelect={(d) => {
            if (!d) return
            const next = new Date(d)
            next.setHours(selected.getHours(), selected.getMinutes(), 0, 0)
            onValueChange(next)
          }}
          className="border-0"
        />
        <div className="border-t border-border p-3">
          <InputGroup>
            <InputGroupInput
              type="time"
              step={60}
              value={value ? toTimeString(value) : "00:00"}
              onChange={(e) => {
                if (!value) return
                const [h, m] = e.target.value.split(":").map(Number)
                const next = new Date(value)
                next.setHours(h || 0, m || 0, 0, 0)
                onValueChange(next)
              }}
              className="appearance-none [&::-webkit-calendar-picker-indicator]:hidden [&::-webkit-calendar-picker-indicator]:appearance-none"
            />
            <InputGroupAddon align="inline-end">
              <ClockIcon className="size-4 text-muted-foreground" />
            </InputGroupAddon>
          </InputGroup>
        </div>
      </PopoverContent>
    </Popover>
  )
}
