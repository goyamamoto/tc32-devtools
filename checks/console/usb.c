/*
 * run-boot --usb attaches the USB device controller model: the program reads
 * the controller's request mode register 0x800104, which the model puts at
 * 0xff at reset (plain storage reads 0 without it), writes its value in hex
 * to the console (register 0xfff0) and calls _exit(), where --stop-at _exit
 * ends the run.
 * Built by checks/console/build.sh with the sem start code.
 * SPDX-License-Identifier: Apache-2.0
 */
#include <stdint.h>

#define CONSOLE  (*(volatile uint8_t *)0x0080fff0u)
#define USB_MODE (*(volatile uint8_t *)0x00800104u)

__attribute__((noinline)) void _exit(int code)
{
	(void)code;
	for (;;) {
	}
}

void sem_main(void)
{
	static const char hex[] = "0123456789abcdef";
	uint8_t v = USB_MODE;

	CONSOLE = (uint8_t)hex[v >> 4];
	CONSOLE = (uint8_t)hex[v & 15u];
	CONSOLE = '\n';
	_exit(0);
}
