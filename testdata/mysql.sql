-- MySQL dump 10.13  Distrib 8.4.3, for Linux (x86_64)
--
-- Host: localhost    Database: billing
-- ------------------------------------------------------
-- Server version	8.4.3

DROP TABLE IF EXISTS `invoices`;
CREATE TABLE `invoices` (
  `id` int NOT NULL,
  `amount` decimal(10,2) DEFAULT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

LOCK TABLES `invoices` WRITE;
INSERT INTO `invoices` VALUES (1,100.00),(2,250.50);
UNLOCK TABLES;

-- Dump completed on 2026-11-08 21:30:00
